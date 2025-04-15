package main

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/elastic/go-elasticsearch/v6"
)

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
		table.WithFocused(true), table.WithHeight(22),
		table.WithStyles(table.Styles{Header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")), Cell: lipgloss.NewStyle().Foreground(lipgloss.Color("251")), Selected: lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236")).Foreground(lipgloss.Color("254"))}))
	shardTbl := table.New(
		table.WithColumns([]table.Column{{Title: "Shard", Width: 6}, {Title: "P/R", Width: 5}, {Title: "State", Width: 15}, {Title: "Docs", Width: 12}, {Title: "Size", Width: 10}, {Title: "Node IP", Width: 15}, {Title: "Node Name", Width: 20}}),
		table.WithFocused(false), table.WithHeight(20),
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
