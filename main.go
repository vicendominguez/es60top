package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
	"github.com/olivere/elastic/v6"
)

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
	Primary     string
	Replicas    string
	DocsCount   string
	StorageSize string // Human-readable size
}

type tickMsg time.Time
type clusterDataMsg struct {
	Cluster   ClusterInfo
	Nodes     []NodeInfo
	Indices   []IndexInfo
	Latency   time.Duration
	Timestamp time.Time
	Err       error
}

// --- Main Model of Bubble Tea ---

type model struct {
	esClient        *elastic.Client
	esURL           string
	refreshInterval time.Duration

	clusterInfo ClusterInfo
	nodeInfo    []NodeInfo
	indexInfo   []IndexInfo
	latency     time.Duration
	lastUpdate  time.Time
	err         error

	spinner   spinner.Model
	isLoading bool

	nodeTable  table.Model
	indexTable table.Model

	// Estilos
	styleBase          lipgloss.Style
	styleHeader        lipgloss.Style
	styleClusterStatus map[string]lipgloss.Style
	styleHelp          lipgloss.Style
}

func initialModel(esURL string, interval time.Duration) model {
	// Spinner
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205")) // spinner

	baseStyle := lipgloss.NewStyle().Padding(0, 1)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")) // Orange for headers

	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241")) // grey

	// Estilos de estado del cluster
	statusStyles := map[string]lipgloss.Style{
		"green":   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")), // Green
		"yellow":  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11")), // Yellow
		"red":     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")),  // Red
		"unknown": lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("7")),  // Grey
	}

	// Tablas
	nodeTbl := table.New(
		table.WithColumns([]table.Column{
			{Title: "Node", Width: 20},
			{Title: "IP", Width: 15},
			{Title: "Roles", Width: 15},
			{Title: "CPU", Width: 5},
			{Title: "RAM", Width: 5},
			{Title: "Heap", Width: 5},
			{Title: "Disk", Width: 20}, // % chat and size
			{Title: "Uptime", Width: 12},
		}),
		table.WithFocused(false), //  not focus to avoid key capture
		table.WithHeight(7),      //  Height
		table.WithStyles(table.Styles{
			Header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")),
			Cell:   lipgloss.NewStyle().Foreground(lipgloss.Color("251")),
		}),
	)

	indexTbl := table.New(
		table.WithColumns([]table.Column{
			{Title: "Index", Width: 35},
			{Title: "Health", Width: 14},
			{Title: "Status", Width: 8},
			{Title: "Docs", Width: 12},
			{Title: "Size", Width: 10},
			{Title: "P", Width: 3}, // Primary shards
			{Title: "R", Width: 3}, // Replica shards
		}),
		table.WithFocused(true),
		table.WithHeight(12),
		table.WithStyles(table.Styles{
			Header:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")),
			Cell:     lipgloss.NewStyle().Foreground(lipgloss.Color("251")),
			Selected: lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236")).Foreground(lipgloss.Color("254")),
		}),
	)

	return model{
		esURL:              esURL,
		refreshInterval:    interval,
		spinner:            s,
		isLoading:          true, // Loading true in the start
		nodeTable:          nodeTbl,
		indexTable:         indexTbl,
		styleBase:          baseStyle,
		styleHeader:        headerStyle,
		styleClusterStatus: statusStyles,
		styleHelp:          helpStyle,
	}
}

func (m model) Init() tea.Cmd {

	return tea.Batch(
		m.connectAndFetchCmd(),
		m.spinner.Tick,
		tickCmd(m.refreshInterval),
	)
}

func (m model) connectAndFetchCmd() tea.Cmd {
	return func() tea.Msg {
		var err error

		// If there is no client or a previous error, try to reconnect

		if m.esClient == nil || m.err != nil {
			m.esClient, err = elastic.NewClient(
				elastic.SetURL(m.esURL),
				elastic.SetSniff(false),       // Deshabilitar sniffing (puede dar problemas)
				elastic.SetHealthcheck(false), // Deshabilitar healthcheck inicial (lo haremos nosotros)
			)
			if err != nil {
				return clusterDataMsg{Err: fmt.Errorf("connection failed: %w", err)}
			}

			// Ping for initial connection verification
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _, err = m.esClient.Ping(m.esURL).Do(ctx)
			if err != nil {
				// Clear client if the ping fails for retry connection
				m.esClient = nil
				return clusterDataMsg{Err: fmt.Errorf("ping failed: %w", err)}
			}
		}

		// If the connection was successful (or already exists), search for data
		return fetchEsData(m.esClient)
	}
}

func tickCmd(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func fetchEsData(client *elastic.Client) tea.Msg {
	start := time.Now()
	ctx := context.Background()

	var clusterInfo ClusterInfo
	var nodeInfoList []NodeInfo
	var indexInfoList []IndexInfo
	var fetchErr error

	// 1. Cluster Health
	healthRes, err := client.ClusterHealth().Do(ctx)
	if err != nil {
		fetchErr = fmt.Errorf("failed getting cluster health: %w", err)
		// No retornamos aquí, intentamos obtener lo que podamos
	} else {
		clusterInfo = ClusterInfo{
			Name:                healthRes.ClusterName,
			Status:              healthRes.Status, // green, yellow, red
			NumberOfNodes:       healthRes.NumberOfNodes,
			NumberOfDataNodes:   healthRes.NumberOfDataNodes,
			ActiveShards:        healthRes.ActiveShards,
			ActivePrimaryShards: healthRes.ActivePrimaryShards,
			InitializingShards:  healthRes.InitializingShards,
			RelocatingShards:    healthRes.RelocatingShards,
			UnassignedShards:    healthRes.UnassignedShards,
			// Uptime is obtained from the nodes
		}
	}

	// 2. Node Stats (para CPU, Mem, Disco, Uptime por nodo)
	nodeStatsRes, err := client.NodesStats().
		Metric("os", "jvm", "fs", "process"). // os: cpu, mem; jvm: heap; fs: disk; process: uptime
		Do(ctx)
	if err != nil {
		if fetchErr != nil {
			fetchErr = fmt.Errorf("%v; failed getting node stats: %w", fetchErr, err)
		} else {
			fetchErr = fmt.Errorf("failed getting node stats: %w", err)
		}
	} else {
		// Search for the uptime of the cluster (use the minimum uptime of the master/data nodes if possible)
		var minUptime int64 = -1
		nodeInfoList = make([]NodeInfo, 0, len(nodeStatsRes.Nodes))

		for id, node := range nodeStatsRes.Nodes {
			roles := strings.Join(node.Roles, ",")
			diskPercent := 0
			var diskTotal, diskFree uint64

			// Calculate aggregated disk usage (there may be multiple mount points)
			if node.FS != nil && node.FS.Total != nil {
				diskTotal = uint64(node.FS.Total.TotalInBytes)
				diskFree = uint64(node.FS.Total.FreeInBytes)
				if diskTotal > 0 {
					diskUsed := diskTotal - diskFree
					diskPercent = int(float64(diskUsed) * 100 / float64(diskTotal))
				}
			}

			nodeUptime := int64(0)
			if node.JVM != nil { // Comprobar si la sección JVM existe
				nodeUptime = node.JVM.UptimeInMillis
			}

			// Actualizar uptime mínimo del cluster (considerando nodos activos)
			if nodeUptime > 0 && (minUptime == -1 || nodeUptime < minUptime) {
				minUptime = nodeUptime
			}

			ni := NodeInfo{
				ID:           id,
				Name:         node.Name,
				IP:           node.Host, // O node.IP si está disponible y es preferible
				Roles:        roles,
				CPUPercent:   -1, // Inicializar por si no vienen los datos
				RAMPercent:   -1,
				HeapPercent:  -1,
				DiskPercent:  diskPercent,
				DiskTotal:    diskTotal,
				DiskFree:     diskFree,
				UptimeMillis: nodeUptime,
			}

			if node.OS != nil && node.OS.CPU != nil {
				ni.CPUPercent = node.OS.CPU.Percent
			}
			if node.OS != nil && node.OS.Mem != nil {
				// ES 6.4 does not always provide direct % RAM, calculate if possible
				// BE CAREFUL: node.OS.Mem.TotalInBytes may be 0 if cgroups are not properly configured
				// We will use 'used_percent' if it is available (more reliable in containers)
				if node.OS.Mem.UsedPercent > 0 {
					ni.RAMPercent = node.OS.Mem.UsedPercent
				} else if node.OS.Mem.TotalInBytes > 0 {
					ni.RAMPercent = int(float64(node.OS.Mem.UsedInBytes) * 100 / float64(node.OS.Mem.TotalInBytes))
				}
			}
			if node.JVM != nil && node.JVM.Mem != nil {
				ni.HeapPercent = node.JVM.Mem.HeapUsedPercent
			}

			nodeInfoList = append(nodeInfoList, ni)
		}
		clusterInfo.UptimeMillis = minUptime
	}

	// 3. Indice Información (usando _cat API para simplificación)
	// h= columnas deseadas, b para obtener bytes
	catIndicesRes, err := client.CatIndices().
		Pretty(true). // Pedir JSON para parsearlo fácilmente
		Columns("health", "status", "index", "uuid", "pri", "rep", "docs.count", "store.size").
		Bytes("b"). // Obtener tamaño en bytes
		Do(ctx)
	if err != nil {
		if fetchErr != nil {
			fetchErr = fmt.Errorf("%v; failed getting indices info: %w", fetchErr, err)
		} else {
			fetchErr = fmt.Errorf("failed getting indices info: %w", err)
		}
	} else {
		indexInfoList = make([]IndexInfo, 0, len(catIndicesRes))

		// Iteramos sobre el slice de filas parseado por el cliente
		for _, indexRow := range catIndicesRes { // indexRow es de tipo CatIndicesResponseRow
			// Accedemos a los datos usando la indexación de mapa.
			// Usamos el "comma-ok" idiom para seguridad por si falta algún campo.
			health := indexRow.Health
			status := indexRow.Status
			name := indexRow.Index
			uuid := indexRow.UUID
			pri := strconv.Itoa(indexRow.Pri)
			rep := strconv.Itoa(indexRow.Rep)
			docsCount := strconv.Itoa(indexRow.DocsCount)
			storeSizeStr := indexRow.StoreSize

			sizeBytes, _ := humanize.ParseBytes(storeSizeStr)

			ii := IndexInfo{
				Health:      health,
				Status:      status,
				Name:        name,
				UUID:        uuid,
				Primary:     pri,
				Replicas:    rep,
				DocsCount:   docsCount,
				StorageSize: humanize.Bytes(sizeBytes), // Mostrar tamaño legible
			}
			indexInfoList = append(indexInfoList, ii)
		}
	}

	latency := time.Since(start)

	return clusterDataMsg{
		Cluster:   clusterInfo,
		Nodes:     nodeInfoList,
		Indices:   indexInfoList,
		Latency:   latency,
		Timestamp: time.Now(),
		Err:       fetchErr,
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd

	m.indexTable, cmd = m.indexTable.Update(msg)
	cmds = append(cmds, cmd)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.isLoading = true
			m.err = nil
			return m, tea.Batch(m.spinner.Tick, m.connectAndFetchCmd())
		}

	case tickMsg:
		var fetchCmd tea.Cmd

		if !m.isLoading {
			m.isLoading = true
			m.err = nil
			fetchCmd = tea.Batch(m.spinner.Tick, m.connectAndFetchCmd())
		}

		nextTickCmd := tickCmd(m.refreshInterval) // Le damos un nombre claro

		cmds = append(cmds, nextTickCmd)

		if fetchCmd != nil {
			cmds = append(cmds, fetchCmd)
		}

	case clusterDataMsg: // Elasticsearch Data
		m.isLoading = false
		m.err = msg.Err
		if msg.Err == nil || !strings.Contains(msg.Err.Error(), "connection failed") && !strings.Contains(msg.Err.Error(), "ping failed") {
			m.clusterInfo = msg.Cluster
			m.nodeInfo = msg.Nodes
			m.indexInfo = msg.Indices
			m.latency = msg.Latency
			m.lastUpdate = msg.Timestamp

			nodeRows := make([]table.Row, len(m.nodeInfo))
			for i, node := range m.nodeInfo {
				uptimeStr := "-"
				if node.UptimeMillis > 0 {
					uptimeStr = formatDuration(time.Duration(node.UptimeMillis) * time.Millisecond)
				}
				diskStr := fmt.Sprintf("%d%% (%s free)", node.DiskPercent, humanize.Bytes(node.DiskFree))
				nodeRows[i] = table.Row{
					node.Name,
					node.IP,
					node.Roles,
					formatPercent(node.CPUPercent),
					formatPercent(node.RAMPercent),
					formatPercent(node.HeapPercent),
					diskStr,
					uptimeStr,
				}
			}
			m.nodeTable.SetRows(nodeRows)

			indexRows := make([]table.Row, len(m.indexInfo))
			for i, index := range m.indexInfo {
				indexRows[i] = table.Row{
					index.Name,
					renderHealth(index.Health, m.styleClusterStatus),
					index.Status,
					index.DocsCount,
					index.StorageSize,
					index.Primary,
					index.Replicas,
				}
			}
			m.indexTable.SetRows(indexRows)
		}

	case spinner.TickMsg: // spinner update
		if m.isLoading {
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}

	case tea.WindowSizeMsg:
		// Adjust the width of tables and other elements if necessary
		// For simplicity, we don't do this here, but it would be important in complex UIs
		H, _ := m.styleBase.GetFrameSize()
		m.nodeTable.SetWidth(msg.Width - H)
		m.indexTable.SetWidth(msg.Width - H)
		return m, tea.Batch(cmds...)
	}
	return m, tea.Batch(cmds...)
}

func (m model) View() string {

	var body strings.Builder

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
	} else if m.clusterInfo.UptimeMillis == 0 {
		uptimeStr = "Calculating..." // Si es 0 pero no -1
	}

	clusterLine1 := fmt.Sprintf("Name: %s | Status: %s | Uptime: %s",
		m.clusterInfo.Name,
		clusterStatusStyle.Render(clusterStatusStr),
		uptimeStr,
	)
	clusterLine2 := fmt.Sprintf("Nodes: %d total, %d data | Shards: %d active, %d primary (%d relocating, %d initializing, %d unassigned)",
		m.clusterInfo.NumberOfNodes, m.clusterInfo.NumberOfDataNodes,
		m.clusterInfo.ActiveShards, m.clusterInfo.ActivePrimaryShards,
		m.clusterInfo.RelocatingShards, m.clusterInfo.InitializingShards, m.clusterInfo.UnassignedShards,
	)
	body.WriteString(clusterLine1 + "\n")
	body.WriteString(clusterLine2 + "\n\n")

	// --- Node section
	body.WriteString(m.styleHeader.Render("Nodes") + "\n")
	body.WriteString(m.nodeTable.View() + "\n\n")

	// --- Indices section
	body.WriteString(m.styleHeader.Render("Indices") + "\n")
	body.WriteString(m.indexTable.View() + "\n\n")

	// footer
	statusLine := " "
	if m.isLoading {
		statusLine += m.spinner.View() + " Refreshing..."
	} else if m.err != nil {

		errorStr := fmt.Sprintf("Error: %s", m.err.Error())
		if len(errorStr) > 80 { // Truncar errores largos
			errorStr = errorStr[:77] + "..."
		}
		statusLine += lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(fmt.Sprintf("Last Update: %s | API Latency: %s",
			m.lastUpdate.Format("15:04:05"),
			errorStr))
	} else {
		statusLine += fmt.Sprintf("Last Update: %s | API Latency: %s",
			m.lastUpdate.Format("15:04:05"),
			m.latency.Truncate(time.Millisecond),
		)
	}

	helpLine := m.styleHelp.Render("Use ↑/↓ PgUp/PgDn Home/End to scroll indices | 'r' to refresh | 'q' to quit")
	// Use JoinHorizontal to align status to the left and help to the right
	// We need to calculate the available space
	// This part is complex to do perfectly without knowing the exact width of the terminal
	footer := lipgloss.JoinHorizontal(lipgloss.Bottom,
		statusLine,
		lipgloss.NewStyle().Width(100).Align(lipgloss.Right).Render(helpLine), // Ajustar Width según necesidad

	)

	body.WriteString(footer)

	return m.styleBase.Render(body.String())
}

// formatDuration: Formatea una duración de forma legible (ej: 2d 3h 4m 5s)
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

// formatPercent: Returns the percentage as a string or "N/A" if it is negative
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

func main() {
	esURL := flag.String("url", "http://localhost:9200", "Elasticsearch cluster URL")
	refreshIntervalStr := flag.String("interval", "5s", "Refresh interval (e.g., 4s, 1m)")
	flag.Parse()

	refreshInterval, err := time.ParseDuration(*refreshIntervalStr)
	if err != nil {
		log.Fatalf("Invalid refresh interval format: %v", err)
	}
	if refreshInterval < 100*time.Millisecond {
		log.Println("Warning: Refresh interval is very short, setting to 500ms minimum.")
		refreshInterval = 100 * time.Millisecond
	}

	p := tea.NewProgram(initialModel(*esURL, refreshInterval))

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}
}
