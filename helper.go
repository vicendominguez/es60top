package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
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
