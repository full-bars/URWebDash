# URWebDash

Wallet and payout stats dashboard for URnetwork providers. Polls the bringyour.com API, stores history in SQLite, and serves a dark-theme Chart.js UI.

## Quick Start

```bash
# Build
go build -o urwebdash .

# Run polling daemon (fetches every 15min on quarter-hour marks)
./urwebdash run

# Run HTTP server (default port 3001)
./urwebdash serve
```

The polling daemon and HTTP server are separate processes. Run both for a full setup.

### Import historical data

```bash
./urwebdash import export.json
```

Accepts a JSON array of records with fields: `paid_bytes_provided`, `unpaid_bytes`, `created_at`, `updated_at`.

## Commands

| Command | Description |
|---|---|
| `run` | Polling daemon — fetches `/account/stats` every 15m, stores in SQLite |
| `serve [port]` | HTTP server — serves dashboard on port 3001 (default) |
| `import <file>` | Import historical records from a JSON file |
| `history` | Print all stored records to stdout |

## Requirements

- Go 1.26+
- JWT token at `~/.urnetwork/jwt` (from a URnetwork provider install)
- No CGO required (uses `modernc.org/sqlite`)

## Environment

| Variable | Default | Description |
|---|---|---|
| `STATS_INTERVAL` | `15m` | Polling interval (min 1m) |
| `JWT_PATH` | `~/.urnetwork/jwt` | Path to JWT token file |
