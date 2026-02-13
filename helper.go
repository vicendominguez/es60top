package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"
)

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

func clamp(val, min, max int) int {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

// parseDocCount converts ES CAT API doc count string to int64
// Handles formats: "1234", "1,234", "", "null"
func parseDocCount(docCountStr string) int64 {
	// Remove commas and whitespace
	cleaned := strings.ReplaceAll(strings.TrimSpace(docCountStr), ",", "")
	if cleaned == "" || cleaned == "null" {
		return 0
	}

	count, err := strconv.ParseInt(cleaned, 10, 64)
	if err != nil {
		return 0 // Graceful fallback
	}
	return count
}

// formatDelta formats document count delta with sign and thousands separator
// Returns styled string for display
func formatDelta(delta int64, styles map[string]lipgloss.Style) string {
	if delta == 0 {
		// No change or first appearance
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("241")) // gray
		return style.Render("—")
	}

	var sign string
	var style lipgloss.Style

	if delta > 0 {
		sign = "+"
		style = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	} else {
		sign = "" // Negative sign already present
		style = lipgloss.NewStyle().Foreground(lipgloss.Color("9")) // red
	}

	// Format with thousands separator
	deltaStr := humanize.Comma(delta) // e.g., "1,234" or "-1,234"
	return style.Render(sign + deltaStr)
}
