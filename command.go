package main

import (
	"context"
	"fmt"
	"io"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/elastic/go-elasticsearch/v6"
)

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
