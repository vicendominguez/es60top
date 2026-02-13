package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const VERSION = "0.0.5"

// --- Main Function ---
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

	// Use AltScreen for better terminal restoration on exit
	p := tea.NewProgram(initialModel(*esURL, refreshInterval), tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running program: %v\n", err)
		os.Exit(1)
	}
}
