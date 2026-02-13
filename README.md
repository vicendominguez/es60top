# Elasticsearch Cluster Top tool (v6)

**Deprecated** tool for monitoring older Elasticsearch clusters (v6.x). Use with caution on legacy systems.

## Features

- **Real-time Cluster Monitoring**: View cluster health, node stats, and index metrics
- **Document Count Delta Tracking**: See document count changes between refresh intervals
  - `+N` in green for documents added
  - `-N` in red for documents removed
  - `—` in gray for no change or first appearance
- **Shard Details**: Drill down into shard allocation by index
- **Auto-Reconnection**: Automatically reconnects on network failures

## Usage
`./es60top --url http://old-cluster:9200 --interval 10s`

- `--url`: Elasticsearch cluster URL (default: `http://localhost:9200`)
- `--interval`: Refresh interval (e.g. `5s`, `1m`)

## Controls
- ↑/↓/PgUp/PgDn/Home/End: Scroll indices
- `Enter`: View shard details for selected index
- `Esc`: Return to main view
- `r`: Manual refresh
- `q`/`Ctrl+c`: Quit

> Note: For Elasticsearch 6.x clusters only

![screenshot-es](/images/screenshot-es.png)

## Install

MacOS  (amd64/arm64): `brew tap vicendominguez/tap` and then `brew install es60top`

## Development

### Prerequisites
- [mise](https://mise.jdx.dev/) - Development task runner
- Docker - For running test Elasticsearch cluster

### Quick Start
```bash
# Setup development environment (starts ES 6.8 container)
mise run dev:setup

# Run es60top against test cluster
mise run run

# Run tests
mise run test

# Run helper function tests
mise run test:helpers
```

### Available Tasks
```bash
# Elasticsearch test cluster
mise run es:start      # Start ES 6.8.23 Docker container
mise run es:stop       # Stop and remove ES container
mise run es:restart    # Restart ES container
mise run es:health     # Check cluster health

# Testing
mise run test          # Run all Go tests
mise run test:helpers  # Run helper function tests only
mise run test:delta    # Run comprehensive delta tracking tests
mise run test:stress   # Stress test with 20+ indices
mise run test:cleanup  # Clean up test indices

# Build and run
mise run build         # Build es60top binary
mise run run           # Run against test cluster
```

### Testing Delta Tracking

1. Start ES test cluster: `mise run es:start`
2. In one terminal, run es60top: `mise run run`
3. In another terminal, run delta tests: `mise run test:delta`
4. Observe the Delta column showing:
   - First appearance: `—` (gray)
   - Documents added: `+100` (green)
   - Documents removed: `-51` (red)

## Tips

1. **Automatic Reconnection**: The system will automatically reconnect if a disconnection occurs.
2. **Disconnection Indicator**: The last updated timestamp before disconnection will remain visible on the screen (to help identify when the issue occurred).
3. **Delta Persistence**: Document count deltas are calculated per refresh interval and persist during connection errors (sticky data).
