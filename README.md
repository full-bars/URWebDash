# URWebDash

Wallet and payout stats dashboard for URnetwork providers. Polls the bringyour.com API, stores history in SQLite, and serves a dark-theme Chart.js UI.

## Features

- **Data Stats** — Real-time paid/unpaid bytes tracking with hourly snapshots, charts, and transfer rate visualization
- **Last 7 Days Usage Strip** — Per-day usage card strip computed from calendar-day buckets (DST-safe)
- **Payout Stats** — Complete payment history with amounts, bytes, points earned per payment, and Solana transaction links
- **Estimated Pending Payouts** — Pending payments show an estimated amount, derived from the booked payout minus the median recent settlement fee
- **Dynamic Network Name** — Sidebar label is decoded from the JWT instead of hardcoded
- **Discord Webhooks** — Automatic notifications when new payments appear or pending payments complete; notification state persists across restarts
- **Auto-Refresh** — Dashboard updates every 30 seconds, payout stats refresh every 5 minutes

## Quick Start

```bash
# Build
go build -o stats_tracker .

# Run polling daemon (fetches every 15min on quarter-hour marks)
./stats_tracker run

# Run HTTP server (default port 3001)
./stats_tracker serve
```

The polling daemon and HTTP server are separate processes. Run both for a full setup.

## Commands

| Command | Description |
|---|---|
| `run` | Polling daemon — fetches `/account/stats` every 15m, stores in SQLite |
| `serve [port]` | HTTP server — serves dashboard on port 3001 (default) |
| `import <file>` | Import historical wallet-stats records from a JSON file. Accepts an array of records with fields `paid_bytes_provided`, `unpaid_bytes`, `created_at`, `updated_at` (optionally `user_id`, `network_name`), or a `{"content": "..."}` wrapper containing that same JSON as a string. |
| `history` | Print all stored records to stdout |
| `cleanup` | Delete off-schedule `wallet_stats` entries for today (keeps only the on-the-quarter-hour rows) |
| `testwebhook` | Send a sample Discord notification to verify `DISCORD_WEBHOOK_URL` is working |

## Requirements

- Go 1.26+
- JWT token at `~/.urnetwork/jwt` (from a URnetwork provider install)
- No CGO required (uses `modernc.org/sqlite`)

## Environment

| Variable | Default | Description |
|---|---|---|
| `STATS_INTERVAL` | `15m` | Polling interval (min 1m) |
| `JWT_PATH` | `~/.urnetwork/jwt` | Path to JWT token file |
| `STATS_DB` | `~/.urnetwork/wallet_stats.db` | Path to the SQLite database |
| `DISCORD_WEBHOOK_URL` | — | Discord webhook URL for payout notifications |
| `PAYOUT_NOTIFY_STORE` | `~/.urnetwork/payout_notified.json` | Path to the payout-notification dedup store |
