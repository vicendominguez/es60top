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

![screenshot-es](/images/screenshot-es.png)

## Install

MacOS  (amd64/arm64): `brew tap vicendominguez/tap` and then `brew install es60top`

## Tricks

1. **Automatic Reconnection:**: The system will automatically reconnect if a disconnection occurs.
2. **Disconnection Indicator**: The last updated timestamp before disconnection will remain visible on the screen (to help identify when the issue occurred).
