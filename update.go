package main

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/dustin/go-humanize"
)

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

			// Initialize delta maps if nil
			if m.previousDocCounts == nil {
				m.previousDocCounts = make(map[string]int64)
				m.indexDeltas = make(map[string]int64)
			}

			// Calculate deltas for current indices
			currentDocCounts := make(map[string]int64)
			for _, index := range msg.Indices {
				// Parse doc count from string (e.g., "1,234" -> 1234)
				currentCount := parseDocCount(index.DocsCount)
				currentDocCounts[index.Name] = currentCount

				// Calculate delta
				if prevCount, exists := m.previousDocCounts[index.Name]; exists {
					m.indexDeltas[index.Name] = currentCount - prevCount
				} else {
					m.indexDeltas[index.Name] = 0 // First appearance, no delta
				}
			}

			// Clean up deltas for disappeared indices
			for indexName := range m.previousDocCounts {
				if _, stillExists := currentDocCounts[indexName]; !stillExists {
					delete(m.previousDocCounts, indexName)
					delete(m.indexDeltas, indexName)
				}
			}

			// Store current counts as previous for next refresh
			m.previousDocCounts = currentDocCounts

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
				indexRows[i] = table.Row{index.Name, renderHealth(index.Health, m.styleClusterStatus), index.Status, index.DocsCount, formatDelta(m.indexDeltas[index.Name], m.styleClusterStatus), index.StorageSize, index.Primary, index.Replicas}
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
	case tea.WindowSizeMsg:
		hMargins := m.styleBase.GetHorizontalPadding()
		contentWidth := msg.Width - hMargins

		m.nodeTable.SetWidth(contentWidth)
		m.indexTable.SetWidth(contentWidth)
		m.shardTable.SetWidth(contentWidth)

		// Altura total disponible menos cabeceras, footer, padding (~10)
		availableHeight := msg.Height - 10
		if availableHeight < 10 {
			availableHeight = 10
		}

		// Altura proporcional para las tablas
		nodeHeight := clamp(availableHeight/3, 5, 20)
		indexHeight := clamp(availableHeight-nodeHeight, 10, 30)
		shardHeight := clamp(availableHeight, 10, 30)

		m.nodeTable.SetHeight(nodeHeight)
		m.indexTable.SetHeight(indexHeight)
		m.shardTable.SetHeight(shardHeight)

		return m, tea.Batch(cmds...)

	} // End switch

	return m, tea.Batch(cmds...)
}
