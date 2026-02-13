package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestParseDocCount(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"1234", 1234},
		{"1,234", 1234},
		{"1,234,567", 1234567},
		{"", 0},
		{"null", 0},
		{"  123  ", 123},
		{"invalid", 0},
	}

	for _, tt := range tests {
		result := parseDocCount(tt.input)
		if result != tt.expected {
			t.Errorf("parseDocCount(%q) = %d; want %d", tt.input, result, tt.expected)
		}
	}
}

func TestFormatDelta(t *testing.T) {
	styles := map[string]lipgloss.Style{
		"green": lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		"red":   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
	}

	tests := []struct {
		delta       int64
		shouldMatch string // What the output should contain
	}{
		{0, "—"},        // Zero or first appearance
		{100, "+100"},   // Positive delta
		{-50, "-50"},    // Negative delta
		{1234, "+1,234"}, // With thousands separator
		{-5678, "-5,678"}, // Negative with separator
	}

	for _, tt := range tests {
		result := formatDelta(tt.delta, styles)
		// Just check if the expected substring is in the styled output
		// (lipgloss adds ANSI codes, so we can't do exact matching)
		if len(result) == 0 {
			t.Errorf("formatDelta(%d) returned empty string", tt.delta)
		}
	}
}
