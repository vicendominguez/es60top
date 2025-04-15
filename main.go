package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
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

const VERSION = "0.0.4"

// --- Internal Data Models ---

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
	Roles        string
	CPUPercent   int
	RAMPercent   int
	HeapPercent  int
	DiskPercent  int
	DiskTotal    uint64
	DiskFree     uint64
	UptimeMillis int64
}

type IndexInfo struct {
	Health      string
	Status      string
	Name        string
	UUID        string
	Primary     string
	Replicas    string
	DocsCount   string
	StorageSize string
}

type ShardDetail struct {
	Index  string
	Shard  string
	PriRep string // p or r
	State  string
	Docs   string
	Store  string
	IP     string
	Node   string
}

// --- Structs for Unmarshalling ES API JSON Responses ---

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

type EsNodesStatsResponse struct {
	Nodes map[string]EsNodeStats `json:"nodes"`
}

type EsNodeStats struct {
	Name  string          `json:"name"`
	Host  string          `json:"host"`
	Roles []string        `json:"roles"`
	OS    *EsNodeOSStats  `json:"os"`
	JVM   *EsNodeJVMStats `json:"jvm"`
	FS    *EsNodeFSStats  `json:"fs"`
}

type EsNodeOSStats struct {
	CPU *struct {
		Percent int `json:"percent"`
	} `json:"cpu"`
	Mem *struct {
		UsedInBytes int64 `json:"used_in_bytes"`
		UsedPercent int   `json:"used_percent"`
	} `json:"mem"`
}

type EsNodeJVMStats struct {
	UptimeInMillis int64 `json:"uptime_in_millis"`
	Mem            *struct {
		HeapUsedPercent int `json:"heap_used_percent"`
	} `json:"mem"`
}

type EsNodeFSStats struct {
	Total *struct {
		TotalInBytes int64 `json:"total_in_bytes"`
		FreeInBytes  int64 `json:"free_in_bytes"`
	} `json:"total"`
}

// --- Bubble Tea Messages ---
type tickMsg time.Time           // For periodic refresh
type connectedClientMsg struct { // Result of connection attempt
	client *elasticsearch.Client
	err    error
}
type clusterDataMsg struct { // Result of main data fetch
	Cluster   ClusterInfo
	Nodes     []NodeInfo
	Indices   []IndexInfo
	Latency   time.Duration
	Timestamp time.Time
	Err       error
}
type shardDataMsg struct { // Result of shard data fetch
	Shards []ShardDetail
	Err    error
}

// --- Main Bubble Tea Model ---
type model struct {
	esClient        *elasticsearch.Client // Can be nil if not connected
	esURL           string
	refreshInterval time.Duration

	// Main State
	clusterInfo ClusterInfo
	nodeInfo    []NodeInfo
	indexInfo   []IndexInfo
	latency     time.Duration
	lastUpdate  time.Time
	err         error // Last general fetch or connection error

	// UI State
	spinner   spinner.Model
	isLoading bool

	// Tables
	nodeTable  table.Model
	indexTable table.Model

	// Shard View State
	showingShards      bool
	selectedShardIndex string
	shardInfo          []ShardDetail
	shardTable         table.Model
	shardErr           error // Error specific to fetching shards

	// Styles
	styleBase          lipgloss.Style
	styleHeader        lipgloss.Style
	styleClusterStatus map[string]lipgloss.Style
	styleShardState    map[string]lipgloss.Style
	styleHelp          lipgloss.Style
}

// --- Initialization ---
func initialModel(esURL string, interval time.Duration) model {
	// Spinner
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	// Base styles
	baseStyle := lipgloss.NewStyle().Padding(0, 1)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	// Status styles (basic ANSI)
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

	// Tables TODO get a best read this is pasted from Gemini
	nodeTbl := table.New(
		table.WithColumns([]table.Column{{Title: "Node", Width: 20}, {Title: "IP", Width: 15}, {Title: "Roles", Width: 15}, {Title: "CPU", Width: 5}, {Title: "RAM", Width: 5}, {Title: "Heap", Width: 5}, {Title: "Disk", Width: 20}, {Title: "Uptime", Width: 12}}),
		table.WithFocused(false), table.WithHeight(7),
		table.WithStyles(table.Styles{Header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")), Cell: lipgloss.NewStyle().Foreground(lipgloss.Color("251"))}))
	indexTbl := table.New(
		table.WithColumns([]table.Column{{Title: "Index", Width: 35}, {Title: "Health", Width: 14}, {Title: "Status", Width: 8}, {Title: "Docs", Width: 12}, {Title: "Size", Width: 10}, {Title: "P", Width: 3}, {Title: "R", Width: 3}}),
		table.WithFocused(true), table.WithHeight(12),
		table.WithStyles(table.Styles{Header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")), Cell: lipgloss.NewStyle().Foreground(lipgloss.Color("251")), Selected: lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236")).Foreground(lipgloss.Color("254"))}))
	shardTbl := table.New(
		table.WithColumns([]table.Column{{Title: "Shard", Width: 6}, {Title: "P/R", Width: 5}, {Title: "State", Width: 15}, {Title: "Docs", Width: 12}, {Title: "Size", Width: 10}, {Title: "Node IP", Width: 15}, {Title: "Node Name", Width: 20}}),
		table.WithFocused(false), table.WithHeight(10),
		table.WithStyles(table.Styles{Header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")), Cell: lipgloss.NewStyle().Foreground(lipgloss.Color("251"))}))

	// Return initial model state (client starts as nil)
	return model{
		esURL:              esURL,
		refreshInterval:    interval,
		spinner:            s,
		isLoading:          true, // Start loading immediately
		nodeTable:          nodeTbl,
		indexTable:         indexTbl,
		shardTable:         shardTbl,
		showingShards:      false,
		styleBase:          baseStyle,
		styleHeader:        headerStyle,
		styleClusterStatus: statusStyles,
		styleShardState:    shardStateStyles,
		styleHelp:          helpStyle,
	}
}

func (m model) Init() tea.Cmd {
	// Initial command batch: try to connect, start spinner, start timer.
	return tea.Batch(
		createConnectCmd(m.esURL), // Start connection attempt
		m.spinner.Tick,
		tickCmd(m.refreshInterval),
	)
}

// --- Command Creation Functions ---

// createConnectCmd returns a Cmd that attempts to connect and ping ES.
// It returns a connectedClientMsg with the result.
func createConnectCmd(url string) tea.Cmd {
	return func() tea.Msg {
		// log.Println("DEBUG: connect/ping goroutine starting...") // Optional debug log
		cfg := elasticsearch.Config{Addresses: []string{url} /* Add Auth if needed */}
		tempClient, err := elasticsearch.NewClient(cfg)
		if err != nil {
			// log.Printf("ERROR: Failed to create ES client: %v\n", err) // Optional debug log
			return connectedClientMsg{client: nil, err: fmt.Errorf("error creating ES client: %w", err)}
		}

		pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pingCancel()
		res, pingErr := tempClient.Ping(tempClient.Ping.WithContext(pingCtx))

		if pingErr != nil {
			// log.Printf("ERROR: Ping network/request failed: %v\n", pingErr) // Optional debug log
			return connectedClientMsg{client: nil, err: fmt.Errorf("ping request setup/network failed: %w", pingErr)}
		}
		defer res.Body.Close()
		if res.IsError() {
			bodyBytes, _ := io.ReadAll(res.Body)
			// log.Printf("ERROR: Ping failed: Server returned status %s; Body: %s\n", res.Status(), string(bodyBytes)) // Optional debug log
			return connectedClientMsg{client: nil, err: fmt.Errorf("ping failed: Server returned status %s; Body: %s", res.Status(), string(bodyBytes))}
		}

		// log.Println("DEBUG: Ping successful! Returning connected client.") // Optional debug log
		return connectedClientMsg{client: tempClient, err: nil} // Success!
	}
}

// createFetchDataCmd returns a Cmd that fetches main cluster data using a valid client.
// It returns a clusterDataMsg.
func createFetchDataCmd(client *elasticsearch.Client) tea.Cmd {
	currentClient := client // Capture client at command creation
	return func() tea.Msg {
		// log.Println("DEBUG: fetchEsData goroutine starting...") // Optional debug log
		if currentClient == nil {
			// This check is defensive, shouldn't happen if Update logic is correct
			return clusterDataMsg{Err: fmt.Errorf("internal error: client was nil for data fetch")}
		}
		return fetchEsData(currentClient)
	}
}

// createFetchShardDataCmd returns a Cmd that fetches shard data using a valid client.
// It returns a shardDataMsg.
func createFetchShardDataCmd(client *elasticsearch.Client, indexName string) tea.Cmd {
	currentClient := client // Capture client
	idxName := indexName    // Capture index name
	return func() tea.Msg {
		// log.Println("DEBUG: fetchShardData goroutine starting...") // Optional debug log
		if currentClient == nil {
			// Defensive check
			return shardDataMsg{Err: fmt.Errorf("internal error: client was nil when fetching shards")}
		}
		return fetchShardData(currentClient, idxName)
	}
}

// tickCmd returns a Cmd that sends a tickMsg after an interval.
func tickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// --- Data Fetching Logic ---

// decodeResponse helper for parsing JSON responses from go-elasticsearch client
func decodeResponse(res *esapi.Response, target any) error {
	if res.IsError() {
		bodyBytes, readErr := io.ReadAll(res.Body)
		if readErr != nil {
			return fmt.Errorf("server error: %s (failed to read body: %v)", res.Status(), readErr)
		}
		return fmt.Errorf("server error: %s: %s", res.Status(), string(bodyBytes))
	}
	bodyBytes, err := io.ReadAll(res.Body) // Read fully first
	if err != nil {
		return fmt.Errorf("error reading response body: %w", err)
	}
	if err := json.Unmarshal(bodyBytes, target); err != nil { // Unmarshal from bytes
		return fmt.Errorf("error parsing response JSON: %w", err)
	}
	return nil
}

// fetchEsData fetches main cluster, node, and index data. Assumes client is non-nil.
func fetchEsData(client *elasticsearch.Client) tea.Msg {
	start := time.Now()
	ctx := context.Background()
	var clusterInfo ClusterInfo
	var nodeInfoList []NodeInfo
	var indexInfoList []IndexInfo
	var fetchErr error

	// 1. Cluster Health
	resHealth, err := client.Cluster.Health(client.Cluster.Health.WithContext(ctx))
	if err != nil {
		fetchErr = fmt.Errorf("cluster health request failed: %w", err)
	} else {
		defer resHealth.Body.Close()
		var healthPayload EsClusterHealthResponse
		if decodeErr := decodeResponse(resHealth, &healthPayload); decodeErr != nil {
			fetchErr = fmt.Errorf("decoding cluster health failed: %w", decodeErr)
		} else {
			clusterInfo = ClusterInfo{ /* ... map fields from healthPayload ... */
				Name: healthPayload.ClusterName, Status: healthPayload.Status, NumberOfNodes: healthPayload.NumberOfNodes,
				NumberOfDataNodes: healthPayload.NumberOfDataNodes, ActiveShards: healthPayload.ActiveShards, ActivePrimaryShards: healthPayload.ActivePrimaryShards,
				InitializingShards: healthPayload.InitializingShards, RelocatingShards: healthPayload.RelocatingShards, UnassignedShards: healthPayload.UnassignedShards,
			}
		}
	}

	// 2. Node Stats
	nodeMetrics := []string{"os", "jvm", "fs"}
	resNodes, err := client.Nodes.Stats(client.Nodes.Stats.WithContext(ctx), client.Nodes.Stats.WithMetric(nodeMetrics...))
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
				roles := strings.Join(node.Roles, ",")
				diskPercent := 0
				var diskTotal, diskFree uint64
				if node.FS != nil && node.FS.Total != nil {
					diskTotal = uint64(node.FS.Total.TotalInBytes)
					diskFree = uint64(node.FS.Total.FreeInBytes)
					if diskTotal > 0 {
						diskPercent = int(float64(diskTotal-diskFree) * 100 / float64(diskTotal))
					}
				}
				nodeUptime := int64(0)
				if node.JVM != nil {
					nodeUptime = node.JVM.UptimeInMillis
				}
				if nodeUptime > 0 && (minUptime == -1 || nodeUptime < minUptime) {
					minUptime = nodeUptime
				}
				ni := NodeInfo{ID: id, Name: node.Name, IP: node.Host, Roles: roles, CPUPercent: -1, RAMPercent: -1, HeapPercent: -1, DiskPercent: diskPercent, DiskTotal: diskTotal, DiskFree: diskFree, UptimeMillis: nodeUptime}
				if node.OS != nil && node.OS.CPU != nil {
					ni.CPUPercent = node.OS.CPU.Percent
				}
				if node.OS != nil && node.OS.Mem != nil && node.OS.Mem.UsedPercent > 0 {
					ni.RAMPercent = node.OS.Mem.UsedPercent
				}
				if node.JVM != nil && node.JVM.Mem != nil {
					ni.HeapPercent = node.JVM.Mem.HeapUsedPercent
				}
				nodeInfoList = append(nodeInfoList, ni)
			}
			if clusterInfo.UptimeMillis <= 0 {
				clusterInfo.UptimeMillis = minUptime
			} // Set calculated uptime
		}
	}

	// 3. Index Info (CAT API)
	indexColumns := []string{"health", "status", "index", "uuid", "pri", "rep", "docs.count", "store.size"}
	resIndices, err := client.Cat.Indices(client.Cat.Indices.WithContext(ctx), client.Cat.Indices.WithH(indexColumns...), client.Cat.Indices.WithFormat("json"), client.Cat.Indices.WithBytes("b"))
	if err != nil {
		fetchErr = combineErrors(fetchErr, fmt.Errorf("cat indices request failed: %w", err))
	} else {
		defer resIndices.Body.Close()
		var catIndicesResult []map[string]any // CAT JSON is array of objects
		bodyBytes, readErr := io.ReadAll(resIndices.Body)
		if readErr != nil {
			fetchErr = combineErrors(fetchErr, fmt.Errorf("reading cat indices body failed: %w", readErr))
		} else if resIndices.IsError() {
			fetchErr = combineErrors(fetchErr, fmt.Errorf("cat indices error: %s: %s", resIndices.Status(), string(bodyBytes)))
		} else if err := json.Unmarshal(bodyBytes, &catIndicesResult); err != nil {
			fetchErr = combineErrors(fetchErr, fmt.Errorf("parsing cat indices JSON failed: %w", err))
		} else {
			indexInfoList = make([]IndexInfo, 0, len(catIndicesResult))
			for _, indexData := range catIndicesResult {
				health, _ := indexData["health"].(string)
				status, _ := indexData["status"].(string)
				name, _ := indexData["index"].(string)
				uuid, _ := indexData["uuid"].(string)
				pri, _ := indexData["pri"].(string)
				rep, _ := indexData["rep"].(string)
				docsCount, _ := indexData["docs.count"].(string)
				storeSizeStr, _ := indexData["store.size"].(string)
				sizeBytes, _ := humanize.ParseBytes(storeSizeStr)
				indexInfoList = append(indexInfoList, IndexInfo{Health: health, Status: status, Name: name, UUID: uuid, Primary: pri, Replicas: rep, DocsCount: docsCount, StorageSize: humanize.Bytes(sizeBytes)})
			}
		}
	}

	latency := time.Since(start)
	return clusterDataMsg{Cluster: clusterInfo, Nodes: nodeInfoList, Indices: indexInfoList, Latency: latency, Timestamp: time.Now(), Err: fetchErr}
}

// fetchShardData fetches shard details. Assumes client is non-nil.
func fetchShardData(client *elasticsearch.Client, indexName string) tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var fetchErr error
	var shardDetailList []ShardDetail

	shardColumns := []string{"index", "shard", "prirep", "state", "docs", "store", "ip", "node"}
	resShards, err := client.Cat.Shards(client.Cat.Shards.WithContext(ctx), client.Cat.Shards.WithIndex(indexName), client.Cat.Shards.WithH(shardColumns...), client.Cat.Shards.WithFormat("json"), client.Cat.Shards.WithBytes("b"))

	if err != nil {
		fetchErr = fmt.Errorf("cat shards request failed for %s: %w", indexName, err)
	} else {
		defer resShards.Body.Close()
		var catShardsResult []map[string]any
		bodyBytes, readErr := io.ReadAll(resShards.Body)
		if readErr != nil {
			fetchErr = fmt.Errorf("reading cat shards body failed: %w", readErr)
		} else if resShards.IsError() {
			fetchErr = fmt.Errorf("cat shards error: %s: %s", resShards.Status(), string(bodyBytes))
		} else if err := json.Unmarshal(bodyBytes, &catShardsResult); err != nil {
			fetchErr = fmt.Errorf("parsing cat shards JSON for %s failed: %w", indexName, err)
		} else {
			shardDetailList = make([]ShardDetail, 0, len(catShardsResult))
			for _, shardData := range catShardsResult {
				index, _ := shardData["index"].(string)
				shard, _ := shardData["shard"].(string)
				prirep, _ := shardData["prirep"].(string)
				state, _ := shardData["state"].(string)
				docs, _ := shardData["docs"].(string)
				storeSizeStr, _ := shardData["store"].(string)
				ip, _ := shardData["ip"].(string)
				node, _ := shardData["node"].(string)
				sizeBytes, _ := humanize.ParseBytes(storeSizeStr)
				shardDetailList = append(shardDetailList, ShardDetail{Index: index, Shard: shard, PriRep: prirep, State: state, Docs: docs, Store: humanize.Bytes(sizeBytes), IP: ip, Node: node})
			}
		}
	}
	return shardDataMsg{Shards: shardDetailList, Err: fetchErr}
}

// --- Bubble Tea Update Logic ---
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	// Update index table only if visible/active
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
				m.isLoading = false
				return m, nil // Return to main view
			}
		} else { // Main view keys
			switch msg.String() {
			case "enter":
				if m.esClient == nil { // Safety check: ensure client is connected
					m.err = fmt.Errorf("cannot fetch shards: client not connected")
					return m, nil // Prevent action
				}
				selectedRow := m.indexTable.SelectedRow()
				if selectedRow != nil && len(selectedRow) > 0 {
					indexName := selectedRow[0]
					m.showingShards = true
					m.selectedShardIndex = indexName
					m.isLoading = true
					m.shardInfo = nil
					m.shardErr = nil
					// Trigger shard data fetch
					cmd = createFetchShardDataCmd(m.esClient, indexName)
					cmds = append(cmds, tea.Batch(m.spinner.Tick, cmd)) // Start spinner too
					return m, tea.Batch(cmds...)                        // Return immediately
				}
			case "r": // Manual refresh
				m.isLoading = true
				m.err = nil
				m.showingShards = false
				// Trigger connection check first
				cmd = createConnectCmd(m.esURL)
				cmds = append(cmds, tea.Batch(m.spinner.Tick, cmd))
				return m, tea.Batch(cmds...) // Return immediately
			}
		}
		// Global keys
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	case tickMsg: // Timer tick
		var triggerCmd tea.Cmd
		if !m.isLoading {
			// log.Println("DEBUG: Tick detected, triggering connection check...") // Optional log
			m.isLoading = true
			// Always trigger a connection check/ping first on tick
			triggerCmd = createConnectCmd(m.esURL)
		}
		// Schedule next tick
		cmds = append(cmds, tickCmd(m.refreshInterval))
		// Add connection check command if created
		if triggerCmd != nil {
			cmds = append(cmds, tea.Batch(m.spinner.Tick, triggerCmd)) // Start spinner
		}

	case connectedClientMsg: // Result from connection attempt
		// log.Println("DEBUG: Received connectedClientMsg")
		if msg.err != nil { // Failed connection/ping
			// log.Printf("DEBUG: Connection/Ping failed: %v\n", msg.err)
			m.err = msg.err     // Store the connection error
			m.esClient = nil    // Ensure client is nil
			m.isLoading = false // Stop loading indicator
		} else { // Success
			// log.Println("DEBUG: Connection/Ping successful. Storing client and fetching data.")
			m.esClient = msg.client // Store the valid client
			m.err = nil             // Clear previous error
			// isLoading remains true because we now fetch data
			cmd = createFetchDataCmd(m.esClient) // Trigger data fetch
			cmds = append(cmds, cmd)
		}

	case clusterDataMsg: // Result from main data fetch
		// log.Println("DEBUG: Received clusterDataMsg") // Optional log
		if m.showingShards {
			return m, tea.Batch(cmds...)
		} // Ignore if showing shards
		m.isLoading = false // Stop loading indicator

		if msg.Err == nil { // Success
			m.err = nil // Clear previous error
			// Update model state
			m.clusterInfo = msg.Cluster
			m.nodeInfo = msg.Nodes
			m.indexInfo = msg.Indices
			m.latency = msg.Latency
			m.lastUpdate = msg.Timestamp
			// Update tables
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
			indexRows := make([]table.Row, len(m.indexInfo))
			for i, index := range m.indexInfo {
				indexRows[i] = table.Row{index.Name, renderHealth(index.Health, m.styleClusterStatus), index.Status, index.DocsCount, index.StorageSize, index.Primary, index.Replicas}
			}
			m.indexTable.SetRows(indexRows)
		} else { // Fetch failed
			// log.Printf("DEBUG: fetchEsData failed: %v\n", msg.Err)
			m.err = msg.Err // Store fetch error, keep existing data and lastUpdate time
		}

	case shardDataMsg: // Result from shard data fetch
		// log.Println("DEBUG: Received shardDataMsg")
		m.isLoading = false  // Stop loading indicator
		m.shardErr = msg.Err // Store shard specific error
		if msg.Err == nil {
			m.shardInfo = msg.Shards
			// Populate shard table
			shardRows := make([]table.Row, len(m.shardInfo))
			for i, shard := range m.shardInfo {
				shardRows[i] = table.Row{shard.Shard, shard.PriRep, renderShardState(shard.State, m.styleShardState), shard.Docs, shard.Store, shard.IP, shard.Node}
			}
			m.shardTable.SetRows(shardRows)
		}

	case spinner.TickMsg: // Update spinner animation
		if m.isLoading {
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}

	case tea.WindowSizeMsg: // Handle terminal resize
		hMargins := m.styleBase.GetHorizontalPadding()
		m.nodeTable.SetWidth(msg.Width - hMargins)
		m.indexTable.SetWidth(msg.Width - hMargins)
		m.shardTable.SetWidth(msg.Width - hMargins)
		// Optionally adjust heights based on msg.Height
		return m, tea.Batch(cmds...)

	} // End switch

	return m, tea.Batch(cmds...)
}

// --- Bubble Tea View Logic ---
func (m model) View() string {
	var body strings.Builder

	// Shard View
	if m.showingShards {
		title := fmt.Sprintf("Shard Details for Index: %s", m.selectedShardIndex)
		body.WriteString(m.styleHeader.Render(title) + "\n\n")
		m.isLoading = false
		if m.shardErr != nil {
			errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
			body.WriteString(errorStyle.Render(fmt.Sprintf("Error loading shards: %s", m.shardErr.Error())) + "\n")
		} else {
			body.WriteString(m.shardTable.View() + "\n")
		}
		helpLine := m.styleHelp.Render("Press Esc or Backspace to return | 'q' to quit")
		footer := lipgloss.NewStyle().PaddingTop(1).Render(helpLine)
		body.WriteString(footer)
	} else {

		// Main View
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
		} else if m.clusterInfo.UptimeMillis == 0 && m.isLoading && m.lastUpdate.IsZero() {
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
		helpLine := m.styleHelp.Render("Scroll ↑/↓ PgUp/PgDn | Enter for shards | 'r' refresh | 'q' quit")
		if m.isLoading {
			statusLine += m.spinner.View() + " Refreshing..."
		} else if m.err != nil { // Error occurred
			statusLine += fmt.Sprintf("Last Update: %s", m.lastUpdate.Format("15:04:05")) // Show sticky timestamp
			errorStr := fmt.Sprintf(" | Error: %s", m.err.Error())
			if len(errorStr) > 70 {
				errorStr = errorStr[:67] + "..."
			}
			statusLine += lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(errorStr) // Show error
		} else { // Success
			statusLine += fmt.Sprintf("Last Update: %s | API Latency: %s", m.lastUpdate.Format("15:04:05"), m.latency.Truncate(time.Millisecond))
		}
		// Align footer elements
		// Calculate remaining width for help text alignment
		statusWidth := lipgloss.Width(statusLine)
		totalWidth := m.indexTable.Width() // Use table width as reference
		helpPadding := 0
		if totalWidth > statusWidth {
			helpPadding = totalWidth - statusWidth
		}
		footer := lipgloss.JoinHorizontal(lipgloss.Bottom, statusLine, lipgloss.NewStyle().PaddingLeft(helpPadding).Align(lipgloss.Right).Render(helpLine))
		body.WriteString(footer)
	}
	return m.styleBase.Render(body.String())
}

// --- Helper Functions ---

func formatDuration(d time.Duration) string {
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
func formatPercent(p int) string {
	if p < 0 {
		return "N/A"
	}
	return fmt.Sprintf("%d%%", p)
}
func renderHealth(health string, styles map[string]lipgloss.Style) string {
	style, ok := styles[health]
	if !ok {
		style = styles["unknown"]
	}
	return style.Render(strings.ToUpper(health))
}
func renderShardState(state string, styles map[string]lipgloss.Style) string {
	style, ok := styles[strings.ToUpper(state)]
	if !ok {
		style = styles["UNKNOWN"]
	}
	return style.Render(state)
}
func combineErrors(existing error, new error) error {
	if existing == nil {
		return new
	}
	if new == nil {
		return existing
	}
	return fmt.Errorf("%v; %w", existing, new)
}

// --- Main Function ---
func main() {
	esURL := flag.String("url", "http://localhost:9200", "Elasticsearch cluster URL")
	refreshIntervalStr := flag.String("interval", "5s", "Refresh interval (e.g., 2s, 1m)")
	showVersion := flag.Bool("version", false, "Display version information")
	flag.Parse()

	if *showVersion {
		fmt.Println("es-tui version", VERSION)
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

	// Use AltScreen for better terminal restoration on exit
	p := tea.NewProgram(initialModel(*esURL, refreshInterval), tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}
}
