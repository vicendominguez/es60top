package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

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
		totalWidth = 80
		helpPadding := 0
		if totalWidth > statusWidth {
			helpPadding = totalWidth - statusWidth
		}
		footer := lipgloss.JoinHorizontal(lipgloss.Bottom, statusLine, lipgloss.NewStyle().PaddingLeft(helpPadding).Align(lipgloss.Right).Render(helpLine))
		body.WriteString(footer)
	}
	return m.styleBase.Render(body.String())
}
