package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/dustin/go-humanize"
	"github.com/elastic/go-elasticsearch/v6"
	"github.com/elastic/go-elasticsearch/v6/esapi"
)

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
