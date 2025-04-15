package main

import (
	"bytes" // Needed for decoding response body
	"context"
	"encoding/json" // Needed for manual JSON parsing
	"flag"
	"fmt"
	"io" // Needed for reading response body
	"log"
	"os"
	"strings"
	"time"

	// Official Elasticsearch Client v6
	"github.com/elastic/go-elasticsearch/v6"
	"github.com/elastic/go-elasticsearch/v6/esapi"

	// Bubble Tea and helpers
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
)

const VERSION = "0.0.3" // Incremented version

// --- Data Models (Structs for our internal state - no change) ---

type ClusterInfo struct {
	Name                string
	Status              string // green, yellow, red
	UptimeMillis        int64
	NumberOfNodes       int
	NumberOfDataNodes   int
	ActiveShards        int
	ActivePrimaryShards int
	InitializingShards  int
	RelocatingShards    int
	UnassignedShards    int
}

type NodeInfo struct {
	ID           string
	Name         string
	IP           string
	Roles        string // master, data, ingest, etc.
	CPUPercent   int
	RAMPercent   int // OS RAM Usage
	HeapPercent  int // JVM Heap Usage
	DiskPercent  int // Disk Usage % for the path.data mount
	DiskTotal    uint64
	DiskFree     uint64
	UptimeMillis int64
}

type IndexInfo struct {
	Health      string // green, yellow, red
	Status      string // open, close
	Name        string
	UUID        string
	Primary     string // Number of primary shards (as string)
	Replicas    string // Number of replica shards (as string)
	DocsCount   string // Document count (as string)
	StorageSize string // Human-readable size
}

type ShardDetail struct {
	Index  string
	Shard  string // Shard number
	PriRep string // p or r
	State  string // STARTED, INITIALIZING, UNASSIGNED, RELOCATING
	Docs   string // Document count (as string)
	Store  string // Store size (humanized)
	IP     string
	Node   string // Node name
}

// --- Structs for Unmarshalling ES API JSON Responses ---

// EsClusterHealthResponse mirrors the structure of the _cluster/health API response
type EsClusterHealthResponse struct {
	ClusterName         string `json:"cluster_name"`
	Status              string `json:"status"`
	NumberOfNodes       int    `json:"number_of_nodes"`
	NumberOfDataNodes   int    `json:"number_of_data_nodes"`
	ActiveShards        int    `json:"active_shards"`
	ActivePrimaryShards int    `json:"active_primary_shards"`
	RelocatingShards    int    `json:"relocating_shards"`
	InitializingShards  int    `json:"initializing_shards"`
	UnassignedShards    int    `json:"unassigned_shards"`
}

// EsNodesStatsResponse mirrors the top level of the _nodes/stats API response
type EsNodesStatsResponse struct {
	Nodes map[string]EsNodeStats `json:"nodes"` // Map node ID to its stats
}

// EsNodeStats mirrors the stats structure for a single node
type EsNodeStats struct {
	Name  string          `json:"name"`
	Host  string          `json:"host"` // Note: In ES 6.x, transport_address might be more reliable than host
	Roles []string        `json:"roles"`
	OS    *EsNodeOSStats  `json:"os"`
	JVM   *EsNodeJVMStats `json:"jvm"`
	FS    *EsNodeFSStats  `json:"fs"`
	// Process *EsNodeProcessStats `json:"process"` // Not currently used
}

type EsNodeOSStats struct {
	CPU *struct {
		Percent int `json:"percent"`
	} `json:"cpu"`
	Mem *struct {
		// TotalInBytes int64 `json:"total_in_bytes"` // Might not be present/reliable in 6.x
		// FreeInBytes  int64 `json:"free_in_bytes"`
		UsedInBytes int64 `json:"used_in_bytes"` // Need used to calculate % if total available
		UsedPercent int   `json:"used_percent"`  // Prefer this if available (cgroups)
	} `json:"mem"`
	// Add other OS stats if needed
}

type EsNodeJVMStats struct {
	UptimeInMillis int64 `json:"uptime_in_millis"`
	Mem            *struct {
		HeapUsedPercent int `json:"heap_used_percent"`
		// HeapUsedInBytes int64 `json:"heap_used_in_bytes"`
		// HeapMaxInBytes  int64 `json:"heap_max_in_bytes"`
	} `json:"mem"`
}

type EsNodeFSStats struct {
	Total *struct {
		TotalInBytes int64 `json:"total_in_bytes"`
		FreeInBytes  int64 `json:"free_in_bytes"`
		// AvailableInBytes int64 `json:"available_in_bytes"`
	} `json:"total"`
	// Data []EsNodeFSData `json:"data"` // More detailed path info if needed
}

// --- Bubble Tea Messages (no change) ---
type tickMsg time.Time
type clusterDataMsg struct {
	Cluster   ClusterInfo
	Nodes     []NodeInfo
	Indices   []IndexInfo
	Latency   time.Duration
	Timestamp time.Time
	Err       error
}
type shardDataMsg struct {
	Shards []ShardDetail
	Err    error
}

// --- Main Bubble Tea Model (change esClient type) ---
type model struct {
	esClient        *elasticsearch.Client // <-- CHANGED TYPE
	esURL           string
	refreshInterval time.Duration

	// Rest of the model fields remain the same
	clusterInfo        ClusterInfo
	nodeInfo           []NodeInfo
	indexInfo          []IndexInfo
	latency            time.Duration
	lastUpdate         time.Time
	err                error
	spinner            spinner.Model
	isLoading          bool
	nodeTable          table.Model
	indexTable         table.Model
	showingShards      bool
	selectedShardIndex string
	shardInfo          []ShardDetail
	shardTable         table.Model
	shardErr           error
	styleBase          lipgloss.Style
	styleHeader        lipgloss.Style
	styleClusterStatus map[string]lipgloss.Style
	styleShardState    map[string]lipgloss.Style
	styleHelp          lipgloss.Style
}

// --- Initialization (update client creation) ---
func initialModel(esURL string, interval time.Duration) model {
	// ... (spinner, styles setup remain the same) ...
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	baseStyle := lipgloss.NewStyle().Padding(0, 1)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	statusStyles := map[string]lipgloss.Style{
		"green":   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")),
		"yellow":  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11")),
		"red":     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")),
		"unknown": lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("7")),
	}
	shardStateStyles := map[string]lipgloss.Style{
		"STARTED":      lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		"RELOCATING":   lipgloss.NewStyle().Foreground(lipgloss.Color("14")),
		"INITIALIZING": lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		"UNASSIGNED":   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		"UNKNOWN":      lipgloss.NewStyle().Foreground(lipgloss.Color("7")),
	}

	// ... (table initialization remains the same) ...
	nodeTbl := table.New(
		table.WithColumns([]table.Column{
			{Title: "Node", Width: 20}, {Title: "IP", Width: 15}, {Title: "Roles", Width: 15},
			{Title: "CPU", Width: 5}, {Title: "RAM", Width: 5}, {Title: "Heap", Width: 5},
			{Title: "Disk", Width: 20}, {Title: "Uptime", Width: 12}}),
		table.WithFocused(false), table.WithHeight(7),
		table.WithStyles(table.Styles{Header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")), Cell: lipgloss.NewStyle().Foreground(lipgloss.Color("251"))}))
	indexTbl := table.New(
		table.WithColumns([]table.Column{
			{Title: "Index", Width: 35}, {Title: "Health", Width: 14}, {Title: "Status", Width: 8},
			{Title: "Docs", Width: 12}, {Title: "Size", Width: 10}, {Title: "P", Width: 3}, {Title: "R", Width: 3}}),
		table.WithFocused(true), table.WithHeight(12),
		table.WithStyles(table.Styles{Header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")), Cell: lipgloss.NewStyle().Foreground(lipgloss.Color("251")), Selected: lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236")).Foreground(lipgloss.Color("254"))}))
	shardTbl := table.New(
		table.WithColumns([]table.Column{
			{Title: "Shard", Width: 6}, {Title: "P/R", Width: 5}, {Title: "State", Width: 15},
			{Title: "Docs", Width: 12}, {Title: "Size", Width: 10}, {Title: "Node IP", Width: 15}, {Title: "Node Name", Width: 20}}),
		table.WithFocused(false), table.WithHeight(10),
		table.WithStyles(table.Styles{Header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")), Cell: lipgloss.NewStyle().Foreground(lipgloss.Color("251"))}))

	// Create the initial model struct (client is initially nil)
	m := model{
		esURL:           esURL,
		refreshInterval: interval,
		spinner:         s,
		isLoading:       true, // Start loading

		nodeTable:  nodeTbl,
		indexTable: indexTbl,
		shardTable: shardTbl,

		showingShards: false,

		styleBase:          baseStyle,
		styleHeader:        headerStyle,
		styleClusterStatus: statusStyles,
		styleShardState:    shardStateStyles,
		styleHelp:          helpStyle,
	}
	// ES client is initialized lazily in connectAndFetchCmd
	return m
}

// Init (no changes needed)
func (m model) Init() tea.Cmd {
	return tea.Batch(
		m.connectAndFetchCmd(),
		m.spinner.Tick,
		tickCmd(m.refreshInterval),
	)
}

// --- Data Fetching Commands (Update client creation and ping) ---

func (m model) connectAndFetchCmd() tea.Cmd {
	return func() tea.Msg {
		var err error
		// If client is nil or previous attempt failed, try to (re)connect.
		if m.esClient == nil || m.err != nil {
			// --- Use go-elasticsearch client creation ---
			cfg := elasticsearch.Config{
				Addresses: []string{m.esURL},
				// Add other configurations like username/password, TLS if needed
				// Transport: &http.Transport{...}
			}
			m.esClient, err = elasticsearch.NewClient(cfg)
			// --- End client creation ---
			if err != nil {
				return clusterDataMsg{Err: fmt.Errorf("error creating ES client: %w", err)}
			}

			// --- Use go-elasticsearch Ping ---
			// Create a context with a timeout for the ping request
			pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer pingCancel() // Ensure the context cancel func is called

			// Make the Ping request
			res, err := m.esClient.Ping(
				// Pass functional options like context if needed
				m.esClient.Ping.WithContext(pingCtx),
				// Other options like WithPretty(), WithHuman(), WithErrorTrace(true) can be added for debugging
			)

			// 1. Check for network/request level errors first
			if err != nil {
				m.esClient = nil // Clear client so we retry connection next time
				return clusterDataMsg{Err: fmt.Errorf("ping request setup/network failed: %w", err)}
			}

			// 2. IMPORTANT: Defer closing the response body *after* checking for network errors
			//    and before checking the response status.
			defer res.Body.Close()

			// 3. Check if Elasticsearch returned an error status code (e.g., 4xx, 5xx)
			if res.IsError() {
				m.esClient = nil // Clear client so we retry connection next time
				// Try to read the response body for more details from Elasticsearch
				bodyBytes, readErr := io.ReadAll(res.Body)
				errorBody := ""
				if readErr == nil {
					errorBody = string(bodyBytes)
				}
				return clusterDataMsg{Err: fmt.Errorf("ping failed: Server returned status %s; Body: %s", res.Status(), errorBody)}
			}
			// --- End Ping ---
		}

		// Fetch main data using the (now validated) client
		return fetchEsData(m.esClient)
	}
}

// fetchShardDataCmd (no change needed in the command itself)
func fetchShardDataCmd(client *elasticsearch.Client, indexName string) tea.Cmd {
	return func() tea.Msg {
		return fetchShardData(client, indexName)
	}
}

// tickCmd (no changes needed)
func tickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// --- Data Fetching Logic (Major Changes for go-elasticsearch) ---

// Helper function to check response and decode JSON
func decodeResponse(res *esapi.Response, target interface{}) error {
	if res.IsError() {
		// Try to read the error body for more details
		bodyBytes, readErr := io.ReadAll(res.Body)
		if readErr != nil {
			// If reading body fails, return the status code error
			return fmt.Errorf("server error: %s", res.Status())
		}
		// Return the error details from the body
		return fmt.Errorf("server error: %s: %s", res.Status(), string(bodyBytes))
	}
	// If not an error, decode the body into the target struct
	// Use bytes.NewReader to avoid potential issues with io.ReadCloser consumption
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %w", err)
	}
	if err := json.NewDecoder(bytes.NewReader(bodyBytes)).Decode(target); err != nil {
		return fmt.Errorf("error parsing response JSON: %w", err)
	}
	return nil
}

func fetchEsData(client *elasticsearch.Client) tea.Msg {
	start := time.Now()
	ctx := context.Background() // Use context if needed for cancellations

	var clusterInfo ClusterInfo
	var nodeInfoList []NodeInfo
	var indexInfoList []IndexInfo
	var fetchErr error

	// --- 1. Cluster Health ---
	resHealth, err := client.Cluster.Health(client.Cluster.Health.WithContext(ctx))
	if err != nil {
		fetchErr = fmt.Errorf("cluster health request failed: %w", err)
	} else {
		defer resHealth.Body.Close()
		var healthPayload EsClusterHealthResponse
		if decodeErr := decodeResponse(resHealth, &healthPayload); decodeErr != nil {
			fetchErr = fmt.Errorf("decoding cluster health failed: %w", decodeErr)
		} else {
			// Map decoded payload to internal ClusterInfo struct
			clusterInfo = ClusterInfo{
				Name:                healthPayload.ClusterName,
				Status:              healthPayload.Status,
				NumberOfNodes:       healthPayload.NumberOfNodes,
				NumberOfDataNodes:   healthPayload.NumberOfDataNodes,
				ActiveShards:        healthPayload.ActiveShards,
				ActivePrimaryShards: healthPayload.ActivePrimaryShards,
				InitializingShards:  healthPayload.InitializingShards,
				RelocatingShards:    healthPayload.RelocatingShards,
				UnassignedShards:    healthPayload.UnassignedShards,
			}
		}
	}

	// --- 2. Node Stats ---
	// Specify desired metrics
	resNodes, err := client.Nodes.Stats(
		client.Nodes.Stats.WithContext(ctx),
		client.Nodes.Stats.WithMetric("os", "jvm", "fs"),
	)
	if err != nil {
		fetchErr = combineErrors(fetchErr, fmt.Errorf("node stats request failed: %w", err))
	} else {
		defer resNodes.Body.Close()
		var nodesPayload EsNodesStatsResponse
		if decodeErr := decodeResponse(resNodes, &nodesPayload); decodeErr != nil {
			fetchErr = combineErrors(fetchErr, fmt.Errorf("decoding node stats failed: %w", decodeErr))
		} else {
			var minUptime int64 = -1
			nodeInfoList = make([]NodeInfo, 0, len(nodesPayload.Nodes))

			for id, node := range nodesPayload.Nodes {
				// Extract roles
				roles := strings.Join(node.Roles, ",")

				// Disk Stats
				diskPercent := 0
				var diskTotal, diskFree uint64
				if node.FS != nil && node.FS.Total != nil {
					diskTotal = uint64(node.FS.Total.TotalInBytes)
					diskFree = uint64(node.FS.Total.FreeInBytes)
					if diskTotal > 0 {
						diskUsed := diskTotal - diskFree
						diskPercent = int(float64(diskUsed) * 100 / float64(diskTotal))
					}
				}

				// Node Uptime (from JVM)
				nodeUptime := int64(0)
				if node.JVM != nil {
					nodeUptime = node.JVM.UptimeInMillis
				}
				if nodeUptime > 0 && (minUptime == -1 || nodeUptime < minUptime) {
					minUptime = nodeUptime
				}

				// Create NodeInfo struct
				ni := NodeInfo{
					ID:           id,
					Name:         node.Name,
					IP:           node.Host, // Adjust if needed (e.g., use transport_address if available)
					Roles:        roles,
					CPUPercent:   -1,
					RAMPercent:   -1,
					HeapPercent:  -1,
					DiskPercent:  diskPercent,
					DiskTotal:    diskTotal,
					DiskFree:     diskFree,
					UptimeMillis: nodeUptime,
				}

				// Populate percentages
				if node.OS != nil && node.OS.CPU != nil {
					ni.CPUPercent = node.OS.CPU.Percent
				}
				if node.OS != nil && node.OS.Mem != nil {
					if node.OS.Mem.UsedPercent > 0 { // Prefer UsedPercent
						ni.RAMPercent = node.OS.Mem.UsedPercent
					}
					// Could add fallback calculation if node.OS.Mem.TotalInBytes becomes available/reliable
				}
				if node.JVM != nil && node.JVM.Mem != nil {
					ni.HeapPercent = node.JVM.Mem.HeapUsedPercent
				}

				nodeInfoList = append(nodeInfoList, ni)
			}
			// Set cluster uptime if not already set (e.g., if health check failed)
			if clusterInfo.UptimeMillis == 0 {
				clusterInfo.UptimeMillis = minUptime
			}
		}
	}

	// --- 3. Index Info (CAT API) ---
	resIndices, err := client.Cat.Indices(
		client.Cat.Indices.WithContext(ctx),
		client.Cat.Indices.WithH("health", "status", "index", "uuid", "pri", "rep", "docs.count", "store.size"), // Specify columns
		client.Cat.Indices.WithFormat("json"), // Request JSON
		client.Cat.Indices.WithBytes("b"),     // Request bytes
	)
	if err != nil {
		fetchErr = combineErrors(fetchErr, fmt.Errorf("cat indices request failed: %w", err))
	} else {
		defer resIndices.Body.Close()
		// CAT API JSON is an array of objects, unmarshal into slice of maps
		var catIndicesResult []map[string]interface{}
		bodyBytes, readErr := io.ReadAll(resIndices.Body)
		if readErr != nil {
			fetchErr = combineErrors(fetchErr, fmt.Errorf("reading cat indices body failed: %w", readErr))
		} else if resIndices.IsError() {
			fetchErr = combineErrors(fetchErr, fmt.Errorf("cat indices error: %s: %s", resIndices.Status(), string(bodyBytes)))
		} else if err := json.Unmarshal(bodyBytes, &catIndicesResult); err != nil {
			fetchErr = combineErrors(fetchErr, fmt.Errorf("parsing cat indices JSON failed: %w", err))
		} else {
			// Process the slice of maps
			indexInfoList = make([]IndexInfo, 0, len(catIndicesResult))
			for _, indexData := range catIndicesResult {
				// Safely extract data from map using type assertions
				health, _ := indexData["health"].(string)
				status, _ := indexData["status"].(string)
				name, _ := indexData["index"].(string)
				uuid, _ := indexData["uuid"].(string)
				pri, _ := indexData["pri"].(string) // Comes as string from JSON map
				rep, _ := indexData["rep"].(string)
				docsCount, _ := indexData["docs.count"].(string)
				storeSizeStr, _ := indexData["store.size"].(string)

				sizeBytes, _ := humanize.ParseBytes(storeSizeStr)

				ii := IndexInfo{
					Health:      health,
					Status:      status,
					Name:        name,
					UUID:        uuid,
					Primary:     pri, // Already strings
					Replicas:    rep,
					DocsCount:   docsCount,
					StorageSize: humanize.Bytes(sizeBytes),
				}
				indexInfoList = append(indexInfoList, ii)
			}
		}
	}

	latency := time.Since(start)

	return clusterDataMsg{
		Cluster:   clusterInfo,
		Nodes:     nodeInfoList,
		Indices:   indexInfoList,
		Latency:   latency,
		Timestamp: time.Now(),
		Err:       fetchErr, // Return combined error
	}
}

// fetchShardData fetches shard details using the official client
func fetchShardData(client *elasticsearch.Client, indexName string) tea.Msg {
	ctx := context.Background()
	var fetchErr error
	var shardDetailList []ShardDetail

	resShards, err := client.Cat.Shards(
		client.Cat.Shards.WithContext(ctx),
		client.Cat.Shards.WithIndex(indexName), // Specify index
		client.Cat.Shards.WithH("index", "shard", "prirep", "state", "docs", "store", "ip", "node"), // Specify columns
		client.Cat.Shards.WithFormat("json"), // Request JSON
		client.Cat.Shards.WithBytes("b"),     // Request bytes
	)

	if err != nil {
		fetchErr = fmt.Errorf("cat shards request failed for index %s: %w", indexName, err)
	} else {
		defer resShards.Body.Close()
		var catShardsResult []map[string]interface{} // Unmarshal into maps
		bodyBytes, readErr := io.ReadAll(resShards.Body)
		if readErr != nil {
			fetchErr = fmt.Errorf("reading cat shards body failed: %w", readErr)
		} else if resShards.IsError() {
			fetchErr = fmt.Errorf("cat shards error: %s: %s", resShards.Status(), string(bodyBytes))
		} else if err := json.Unmarshal(bodyBytes, &catShardsResult); err != nil {
			fetchErr = fmt.Errorf("parsing cat shards JSON for index %s failed: %w", indexName, err)
		} else {
			shardDetailList = make([]ShardDetail, 0, len(catShardsResult))
			for _, shardData := range catShardsResult {
				// Extract data safely from map
				index, _ := shardData["index"].(string)
				shard, _ := shardData["shard"].(string)
				prirep, _ := shardData["prirep"].(string)
				state, _ := shardData["state"].(string)
				docs, _ := shardData["docs"].(string) // Docs count comes as string
				storeSizeStr, _ := shardData["store"].(string)
				ip, _ := shardData["ip"].(string)
				node, _ := shardData["node"].(string)

				sizeBytes, _ := humanize.ParseBytes(storeSizeStr)

				sd := ShardDetail{
					Index:  index,
					Shard:  shard,
					PriRep: prirep,
					State:  state,
					Docs:   docs, // Already string
					Store:  humanize.Bytes(sizeBytes),
					IP:     ip,
					Node:   node,
				}
				shardDetailList = append(shardDetailList, sd)
			}
		}
	}

	return shardDataMsg{
		Shards: shardDetailList,
		Err:    fetchErr,
	}
}

// --- Bubble Tea Update Logic (largely unchanged logic, but data comes differently) ---
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	// Pass messages to index table if active
	if !m.showingShards {
		m.indexTable, cmd = m.indexTable.Update(msg)
		cmds = append(cmds, cmd)
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Shard view keys
		if m.showingShards {
			switch msg.String() {
			case "esc", "backspace":
				m.showingShards = false
				m.selectedShardIndex = ""
				m.shardInfo = nil
				m.shardErr = nil
				return m, nil
			}
		}
		// Main view keys
		if !m.showingShards {
			switch msg.String() {
			case "enter":
				if m.esClient == nil {
					m.shardErr = fmt.Errorf("cannot fetch shards: client not connected")
					m.err = fmt.Errorf("cannot fetch shards: client not connected")
					return m, nil // No hacer nada más si el cliente no está listo
				}
				selectedRow := m.indexTable.SelectedRow()
				if selectedRow != nil && len(selectedRow) > 0 {
					indexName := selectedRow[0]
					m.showingShards = true
					m.selectedShardIndex = indexName
					m.isLoading = true
					m.shardInfo = nil
					m.shardErr = nil
					// Use the existing client, connectAndFetchCmd ensures it's valid
					return m, tea.Batch(m.spinner.Tick, fetchShardDataCmd(m.esClient, indexName))
				}
			case "r":
				m.isLoading = true
				m.err = nil
				m.showingShards = false
				// Reconnect/fetch main data
				return m, tea.Batch(m.spinner.Tick, m.connectAndFetchCmd())
			case "s":
				m.isLoading = true
				m.err = nil
				m.showingShards = true
				// Reconnect/fetch main data
				return m, tea.Batch(m.spinner.Tick, m.connectAndFetchCmd())
			}
		}
		// Global keys
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	case tickMsg:
		var fetchCmd tea.Cmd
		if !m.isLoading && !m.showingShards {
			m.isLoading = true
			m.err = nil
			// Reconnect/fetch main data
			fetchCmd = tea.Batch(m.spinner.Tick, m.connectAndFetchCmd())
		}
		nextTickCmd := tickCmd(m.refreshInterval)
		cmds = append(cmds, nextTickCmd)
		if fetchCmd != nil {
			cmds = append(cmds, fetchCmd)
		}

	case clusterDataMsg: // Received main data (parsed in fetchEsData)
		if m.showingShards {
			return m, tea.Batch(cmds...) // Ignore if showing shards
		}
		m.isLoading = false
		// Update model state from message payload
		// Note: Population logic depends on successful parsing within fetchEsData now
		if msg.Err == nil {
			m.clusterInfo = msg.Cluster
			m.nodeInfo = msg.Nodes
			m.indexInfo = msg.Indices
			m.latency = msg.Latency
			m.lastUpdate = msg.Timestamp

			// Update Node Table Rows (no change here)
			nodeRows := make([]table.Row, len(m.nodeInfo))
			for i, node := range m.nodeInfo {
				uptimeStr := "-"
				if node.UptimeMillis > 0 {
					uptimeStr = formatDuration(time.Duration(node.UptimeMillis) * time.Millisecond)
				}
				diskStr := fmt.Sprintf("%d%% (%s free)", node.DiskPercent, humanize.Bytes(node.DiskFree))
				nodeRows[i] = table.Row{node.Name, node.IP, node.Roles, formatPercent(node.CPUPercent), formatPercent(node.RAMPercent), formatPercent(node.HeapPercent), diskStr, uptimeStr}
			}
			m.nodeTable.SetRows(nodeRows)

			// Update Index Table Rows (no change here)
			indexRows := make([]table.Row, len(m.indexInfo))
			for i, index := range m.indexInfo {
				indexRows[i] = table.Row{index.Name, renderHealth(index.Health, m.styleClusterStatus), index.Status, index.DocsCount, index.StorageSize, index.Primary, index.Replicas}
			}
			m.indexTable.SetRows(indexRows)
		} else {
			m.err = msg.Err
		}
	case shardDataMsg: // Received shard data (parsed in fetchShardData)
		m.isLoading = false
		m.shardErr = msg.Err // Store shard fetch error
		if msg.Err == nil {
			m.shardInfo = msg.Shards
			// Populate shard table (no change here)
			shardRows := make([]table.Row, len(m.shardInfo))
			for i, shard := range m.shardInfo {
				shardRows[i] = table.Row{shard.Shard, shard.PriRep, renderShardState(shard.State, m.styleShardState), shard.Docs, shard.Store, shard.IP, shard.Node}
			}
			m.shardTable.SetRows(shardRows)
		}

	case spinner.TickMsg:
		if m.isLoading {
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}

	case tea.WindowSizeMsg:
		hMargins, _ := m.styleBase.GetFrameSize()
		m.nodeTable.SetWidth(msg.Width - hMargins)
		m.indexTable.SetWidth(msg.Width - hMargins)
		m.shardTable.SetWidth(msg.Width - hMargins)
		return m, tea.Batch(cmds...)

	} // End switch

	return m, tea.Batch(cmds...)
}

// --- Bubble Tea View Logic (minor adjustments for clarity) ---
func (m model) View() string {
	var body strings.Builder

	// Shard View
	if m.showingShards {
		title := fmt.Sprintf("Shard Details for Index: %s", m.selectedShardIndex)
		body.WriteString(m.styleHeader.Render(title) + "\n\n")
		if m.isLoading {
			body.WriteString(m.spinner.View() + " Loading shards...\n")
		} else if m.shardErr != nil {
			errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9")) // Red
			body.WriteString(errorStyle.Render(fmt.Sprintf("Error loading shards: %s", m.shardErr.Error())) + "\n")
		} else {
			body.WriteString(m.shardTable.View() + "\n")
		}
		helpLine := m.styleHelp.Render("Press Esc or Backspace to return | 'q' to quit")
		footer := lipgloss.NewStyle().PaddingTop(1).Render(helpLine)
		body.WriteString(footer)

		// Main View
	} else {
		// Cluster Info
		body.WriteString(m.styleHeader.Render("Cluster Info") + "\n")
		clusterStatusStr := "UNKNOWN"
		clusterStatusStyle := m.styleClusterStatus["unknown"]
		if m.clusterInfo.Status != "" {
			clusterStatusStr = strings.ToUpper(m.clusterInfo.Status)
			if style, ok := m.styleClusterStatus[m.clusterInfo.Status]; ok {
				clusterStatusStyle = style
			}
		}
		uptimeStr := "N/A"
		if m.clusterInfo.UptimeMillis > 0 {
			uptimeStr = formatDuration(time.Duration(m.clusterInfo.UptimeMillis) * time.Millisecond)
		} else if m.clusterInfo.UptimeMillis == 0 && m.isLoading && m.lastUpdate.IsZero() { // Show calculating only on very first load
			uptimeStr = "Calculating..."
		}
		clusterLine1 := fmt.Sprintf("Name: %s | Status: %s | Uptime: %s", m.clusterInfo.Name, clusterStatusStyle.Render(clusterStatusStr), uptimeStr)
		clusterLine2 := fmt.Sprintf("Nodes: %d total, %d data | Shards: %d active, %d primary (%d relocating, %d initializing, %d unassigned)", m.clusterInfo.NumberOfNodes, m.clusterInfo.NumberOfDataNodes, m.clusterInfo.ActiveShards, m.clusterInfo.ActivePrimaryShards, m.clusterInfo.RelocatingShards, m.clusterInfo.InitializingShards, m.clusterInfo.UnassignedShards)
		body.WriteString(clusterLine1 + "\n")
		body.WriteString(clusterLine2 + "\n\n")

		// Nodes Table
		body.WriteString(m.styleHeader.Render("Nodes") + "\n")
		body.WriteString(m.nodeTable.View() + "\n\n")

		// Indices Table
		body.WriteString(m.styleHeader.Render("Indices") + "\n")
		body.WriteString(m.indexTable.View() + "\n\n")

		// Footer
		statusLine := " "
		if m.isLoading {
			statusLine += m.spinner.View() + " Refreshing..."
		} else if m.err != nil { // Show general fetch error if present
			errorStr := fmt.Sprintf(" | Error: %s", m.err.Error())
			if len(errorStr) > 60 {
				errorStr = errorStr[:57] + "..."
			}
			statusLine += lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(fmt.Sprintf("Last Update: %s %s", m.lastUpdate.Format("15:04:05"), errorStr))
		} else {
			statusLine += fmt.Sprintf("Last Update: %s | API Latency: %s", m.lastUpdate.Format("15:04:05"), m.latency.Truncate(time.Millisecond))

		}
		helpLine := m.styleHelp.Render("Scroll ↑/↓ PgUp/PgDn | Enter for shards | 'r' refresh | 'q' quit")
		footer := lipgloss.JoinHorizontal(lipgloss.Bottom,
			statusLine,
			lipgloss.NewStyle().Width(m.indexTable.Width()-lipgloss.Width(statusLine)).Align(lipgloss.Right).Render(helpLine),
		)
		body.WriteString(footer)
	}

	return m.styleBase.Render(body.String())
}

// --- Helper Functions ---

// formatDuration (no changes)
func formatDuration(d time.Duration) string {
	// ... same as before ...
	if d <= 0 {
		return "N/A"
	}
	days := int64(d.Hours() / 24)
	d -= time.Duration(days) * 24 * time.Hour
	hours := int64(d.Hours())
	d -= time.Duration(hours) * time.Hour
	minutes := int64(d.Minutes())
	d -= time.Duration(minutes) * time.Minute
	seconds := int64(d.Seconds())
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", seconds))
	}
	if len(parts) == 0 {
		return "0s"
	}
	return strings.Join(parts, " ")
}

// formatPercent (no changes)
func formatPercent(p int) string {
	if p < 0 {
		return "N/A"
	}
	return fmt.Sprintf("%d%%", p)
}

// renderHealth (no changes)
func renderHealth(health string, styles map[string]lipgloss.Style) string {
	style, ok := styles[health]
	if !ok {
		style = styles["unknown"]
	}
	return style.Render(strings.ToUpper(health))
}

// renderShardState (no changes)
func renderShardState(state string, styles map[string]lipgloss.Style) string {
	style, ok := styles[strings.ToUpper(state)]
	if !ok {
		style = styles["UNKNOWN"]
	}
	return style.Render(state)
}

// combineErrors helper (optional, for cleaner error accumulation)
func combineErrors(existing error, new error) error {
	if existing == nil {
		return new
	}
	if new == nil {
		return existing
	}
	// Combine errors, perhaps just concatenating strings or using a multi-error wrapper
	return fmt.Errorf("%v; %w", existing, new)
}

// --- Main Function (no changes except AltScreen) ---
func main() {
	esURL := flag.String("url", "http://localhost:9200", "Elasticsearch cluster URL")
	refreshIntervalStr := flag.String("interval", "5s", "Refresh interval (e.g., 2s, 1m)")
	showVersion := flag.Bool("version", false, "Display version information")
	flag.Parse()

	if *showVersion {
		fmt.Println("es60top version", VERSION)
		os.Exit(0)
	}

	refreshInterval, err := time.ParseDuration(*refreshIntervalStr)
	if err != nil {
		log.Fatalf("Invalid refresh interval format: %v. Use formats like '2s', '1m30s'.", err)
	}
	minInterval := 500 * time.Millisecond
	if refreshInterval < minInterval {
		log.Printf("Warning: Refresh interval %v is very short, setting to %v minimum.", refreshInterval, minInterval)
		refreshInterval = minInterval
	}

	p := tea.NewProgram(initialModel(*esURL, refreshInterval), tea.WithAltScreen()) // Use AltScreen

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}
}
