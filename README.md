# Elasticsearch Cluster Top tool (v6)

**Deprecated** tool for monitoring older Elasticsearch clusters (v6.x). Use with caution on legacy systems.

## Usage
`./es60top -url http://old-cluster:9200 -interval 10s`

- `-url`: Elasticsearch cluster URL (default: `http://localhost:9200`)
- `-interval`: Refresh interval (e.g. `5s`, `1m`)

## Controls
- ↑/↓/PgUp/PgDn/Home/End: Scroll indices
- `r`: Refresh
- `q`/`Ctrl+c`: Quit

> Note: For Elasticsearch 6.x clusters only
