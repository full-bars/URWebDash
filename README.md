# URWebDash

Self-hosted wallet and payout stats dashboard for [URnetwork](https://ur.io) providers. Polls the bringyour.com API, stores history in local SQLite, and serves a dark-theme dashboard.

![dashboard screenshot](https://i.postimg.cc/d3wcVFsg/image.png)

**Features**

- Served bytes from the server's per-epoch figures, which arrive about hourly and two to three hours behind live traffic: total served, this week, the open epoch, last 24 hours, a weekly chart and a transfer rate chart, with byte counts auto-scaled between GB and TB. The old paid and unpaid counters stopped moving when USDC payouts ended and stay as an archive
- Per-day served totals for the last 7 days, and every time on the page follows the browser's timezone (Discord alerts use it too)
- Full payment history: amounts, points per payment, Solana tx links
- Estimated amounts for pending payouts
- Optional Discord notifications on new/completed payments and traffic spikes — configured from the dashboard's 🔔 Webhook section or the `DISCORD_WEBHOOK_URL` env var
- Configurable webhook notification layouts (10 formats, defaulting to an iOS-safe Discord rich embed) persisted via `GET/POST /api/webhook-style` or the `WEBHOOK_STYLE` env var
- Live block progression: the active theme card (blocks done, remaining blocks, epoch percentage) advances on every 12-second chain block without a page refresh
- Real-time dashboard poller (`GET /api/dash-data`) returning live epoch geometry, wallet balances, all-time points, alpha status, prices, and traffic metrics; the frontend polls every 15 seconds to sync open tabs
- Dynamic price trend percentage on the Bittensor price chart that recalculates per active timeframe (1H, 24H, 7D, 30D)
- Mexican Peso (`MXN`, `Mex$`) currency support backed by live CoinGecko FX rates
- Dynamic data stats header: live row counts, date ranges, and polling timestamps instead of a hardcoded snapshot
- Dynamic epoch subtitle showing the live epoch number and completion percentage
- Synchronized manual Refresh button that refreshes epoch data, dashboard data, and traffic stats together
- Auto-refresh: data stats every 20s, payout stats every 5m

## Install

Linux / macOS / WSL2:

```bash
curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/master/install.sh | bash
```

The installer downloads the binary and runs `urwebdash setup` as your regular user (no sudo). Setup:

1. Prompts for an [auth code](https://ur.io), but only if you have no session token yet.
2. Optionally asks for a Discord webhook URL and a traffic spike threshold. You can also add or change the webhook later in the dashboard under 🔔 Webhook.
3. Auto-starts the poller and dashboard, and adds `~/.local/bin` to your PATH in your shell rc.

When it finishes it starts the dashboard and prints the URL. The first poll happens immediately, so charts populate right away. The dashboard binds loopback only; for remote access see [Hosting options](#hosting-options).


### Updating

Re-run the install command above. It replaces the binary and restarts services if installed. Data survives untouched.

### Uninstall

```bash
curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/master/uninstall.sh | bash
```

It asks before deleting data and never touches your URnetwork JWT.

## System services

By default `urwebdash setup` runs the poller and dashboard directly under your user session. To run them as systemd services instead, so they survive reboots and restart on failure:

```bash
sudo ~/.local/bin/urwebdash setup --install-services
```

Use the full path. Sudo does not see `~/.local/bin`.

This installs two units, `urwebdash-run.service` (poller) and `urwebdash-serve.service` (dashboard), managed the normal way:

```bash
sudo systemctl status urwebdash-run urwebdash-serve
sudo systemctl restart urwebdash-run urwebdash-serve
```

## Hosting options

The dashboard is unauthenticated by design. To reach it remotely, put something in front:

- **Tailscale**: install it, done. Dashboard reachable only on your tailnet. `sudo tailscale serve --bg 3001` adds HTTPS with a stable name.
- **Cloudflare Tunnel**: public hostname without open ports. Add Cloudflare Access for auth.
- **Caddy + basic auth** on a VPS with a domain:

```caddy
# /etc/caddy/Caddyfile
dash.example.com {
    # hash: caddy hash-password  (bcrypt; htpasswd -nB also works)
    basic_auth {
        admin $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
    }
    reverse_proxy localhost:3001
}
```

To let your own home IP skip the password:

```caddy
dash.example.com {
    @trusted remote_ip 203.0.113.7

    handle @trusted {
        reverse_proxy localhost:3001
    }
    handle {
        basic_auth {
            admin $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
        }
        reverse_proxy localhost:3001
    }
}
```

## Docker

One container runs everything: polling in the background, dashboard in the foreground.

```bash
docker run -d --name urwebdash \
  -p 127.0.0.1:3001:3001 \
  -v urwebdash-data:/data \
  -e URNETWORK_AUTH_CODE='AUTH_CODE_HERE' \
  ghcr.io/full-bars/urwebdash
```

Replace `AUTH_CODE_HERE` with a real code from https://ur.io (keep the single quotes). The code is exchanged once on first start, then removable from the container config.

The Discord webhook can be set in `.env` (`DISCORD_WEBHOOK_URL`) or configured later in the dashboard's 🔔 Webhook section — the URL is saved to `/data/.urwebdash/discord_webhook` and persists in the volume across container recreation. If both are set, the env var wins.

For Compose instead of plain `docker run`:

```bash
mkdir urwebdash && cd urwebdash
curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/master/example.env -o .env
curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/master/docker-compose.yml -o docker-compose.yml
$EDITOR .env   # every line optional and commented
docker compose up -d
```

Auth options in Compose (pick one):

1. Set `URNETWORK_AUTH_CODE` in `.env`.
2. Uncomment the `~/.urnetwork/jwt:/host-jwt:ro` mount. The file is copied into the volume once; the host copy is never written.

All state lives in the `urwebdash-data` volume (or `./data` with Compose). Back up the data and `.env` together, neither alone is complete.

Port binding controls exposure (`0.0.0.0` inside the container is normal):

| Mapping | Reachable from |
|---|---|
| `127.0.0.1:3001:3001` *(default)* | this machine only |
| `192.168.0.100:3001:3001` | devices on your LAN only |
| `100.x.y.z:3001:3001` | your tailnet only |
| `3001:3001` | all interfaces, public too if the host has a public IP or port-forward |

Docker uninstall: `docker rm -f urwebdash`, add `-v urwebdash-data` to also drop the data.

## Reference

Commands (`urwebdash <command>`):

| Command | Description |
|---|---|
| `run` | Polling daemon. Fetches stats and served bytes every hour on the hour |
| `serve [port]` | HTTP server. Dashboard on port 3001 by default |
| `import <file>` | Import wallet-stats history from a JSON export |
| `history` | Print stored history |
| `cleanup` | Delete off-schedule entries for today |
| `testwebhook` | Send a sample Discord notification |

Environment variables:

| Variable | Default | Description |
|---|---|---|
| `STATS_INTERVAL` | `1h` | Polling interval, minimum 1m. The server reports served bytes about hourly, so a shorter interval mostly repeats the same figure |
| `JWT_PATH` | `~/.urnetwork/jwt` | URnetwork session token file |
| `STATS_DB` | `~/.urwebdash/wallet_stats.db` | SQLite database path |
| `DISCORD_WEBHOOK_URL` | *(unset)* | Webhook for alerts, or `~/.urwebdash/discord_webhook` file (Docker: `/data/.urwebdash/discord_webhook`). Get one: Server Settings -> Integrations -> Webhooks. Also configurable in the dashboard under 🔔 Webhook; the env var wins if both are set |
| `SPIKE_THRESHOLD` | `1GB` | Traffic delta per poll window (`STATS_INTERVAL`, one hour by default) that triggers an alert. An interval of another length is scaled to one window before it is compared. Accepts `500M`, `0.5G`, `1.5GB`, plain bytes |
| `WEBHOOK_STYLE` | `embed` | Webhook notification layout (`embed`, `hero-card`, `ascii-table`, `plain-lines`, `bullets`, `local-clock`, `story`, `quote`, `ansi`, `box-table`). Saved choice in `~/.urwebdash/webhook_style` wins only when this is unset |
| `PAYOUT_NOTIFY_STORE` | `~/.urwebdash/payout_notified.json` | Notification dedup store |
| `HOST` | `127.0.0.1` | Listen address. Inside Docker set `0.0.0.0`; exposure is controlled by the port mapping |
| `EPOCH_FIRST_V2` | `4` | First SN25 epoch that settled under the v2 payout model. Epochs at or after it are labelled `v2`, earlier ones `v1` |

### Bittensor (SN25) data

Two tables, separate from `wallet_stats`, which is never modified by this path:

| Table | Key | Written |
|---|---|---|
| `epoch_stats` | epoch number | hourly, overwritten in place |
| `sn_epoch_state` | single row | hourly |

Epochs are keyed by number because an open epoch reports partial points; a row has to be
correctable until the epoch closes. The traffic table is keyed by time for the opposite
reason, so the two never share a merge or cleanup path.

`/sn/epoch` reports `commit_deadline_block` and `finalize_block` but not the end of the
earning window, so that boundary is derived as `start_block + t_epoch_blocks` (falling
back to `commit - 1200` when the length is missing).

| Endpoint | Returns |
|---|---|
| `GET /api/epochs` | Finalized epoch history plus the live epoch geometry |
| `POST /api/refresh-epochs` | Forces a fetch, then returns the same document |

Epoch data refreshes hourly: an epoch is seven days long, so a faster interval
would only repeat the same request.

### Served bytes

Provider payouts in USDC ended on 2026-11-13, so `unpaid_bytes_provided` is always 0.
`GET /account/epochs` now reports the bytes the network served that the weekly operator-block
points are shared by: `served_byte_count` for each finalized epoch, plus `current_epoch` and
`current_block` for the open epoch and the current Sunday to Sunday block. The running
figures are about two to three hours behind live traffic. Each carries `as_of_millis`, the
time the traffic is counted through.

The poller reads these figures every `STATS_INTERVAL` and keeps one `block_samples` row per
distinct as-of time. The bytes between two samples of the same block are one interval, with the
span taken from the server's as-of times. A change of block has no measurable interval, and a
figure that goes down is shown as a server revision, not as traffic.

The Data Stats page replaces the old paid and unpaid boxes and charts, which stopped moving when
USDC payouts ended, with figures that keep counting:

| Component | What it shows |
|---|---|
| Total served | The old paid plus unpaid counter at a week boundary, plus every week the server has counted since. The two count the same bytes and agree to within a fraction of a gigabyte where they overlap |
| Served this week, Open epoch | The running week and the finished weeks of the open epoch |
| Last 24 hours | Newest sample against one a day earlier in the same week |
| Per day | Bytes per local calendar day, with the hours counted |
| Served per week | One bar per week for the whole history. Weeks before the switch come from the old counter and later weeks from the server |
| Transfer rate | Bytes per second for each measured interval |

The join point is found from the data. The old counter and the server's weekly figures count the
same bytes, so the join is the latest week where the counter's growth matches that week's
`served_byte_count` (within half a percent, at least 2 GB). A week the old counter cannot be read for
never matches, so the join falls back to an older matching week and the server's later weeks fill the
rest. If no week matches, the last Sunday before the old counter dropped by a tenth or more (and at least a gigabyte) is used
and the total is marked as using that weaker rule. With neither, the total is the server's figures alone.

The stored poll windows stay in an archive section. It ends one row after the drop, so the transition
row is visible and the rows of zeros after it are not. They stay in the database.

Every epoch on the page has its date range next to it, in the browser's timezone.

Every time on the page is in the browser's own timezone. The page tells the server that zone once,
and the server uses it for the text in Discord alerts. The alert examples on the Webhook tab and
the test message use the latest real interval. The spike alert compares the interval scaled to one poll
window with `SPIKE_THRESHOLD`, skips block changes, server revisions and gaps over six hours, and
says which time the traffic is counted through.

| Endpoint | Returns |
|---|---|
| `GET /api/served` | Newest sample, the last eight days of intervals, the total and the weekly series |
| `GET /api/alert-sample` | The latest real interval, for the alert examples |
| `GET`, `POST /api/timezone` | The zone alerts are written in. The page sets it |

Rows recorded at the old 15 minute default stay as they are. `urwebdash cleanup` is a manual
command that deletes only today's rows that sit on neither the configured grid nor the 15 minute grid.

### Dashboard data endpoints

| Endpoint | Returns |
|---|---|
| `GET /api/dash-data` | Live epoch geometry, wallet balances, all-time points, alpha status, prices, and traffic metrics |
| `GET /api/webhook-style` | Active webhook notification layout |
| `POST /api/webhook-style` | Persist a new webhook notification layout |

Troubleshooting:

| Symptom | Fix |
|---|---|
| `read jwt: no such file or directory` | No JWT at `~/.urnetwork/jwt`. Re-run the installer or copy one from your provider machine. |
| Empty charts | The poller fetches immediately on start. If still empty, wait for the next hour or lower `STATS_INTERVAL`. |
| API errors after a while | JWT expired or rotated. Get a fresh one and restart. |
| Port already in use | `urwebdash serve 3002` |
| No Discord notifications | Run `urwebdash testwebhook`, or open the dashboard's 🔔 Webhook section and click 📣 Send Test — it reports Discord's actual response. |
| Duplicate rows today | `urwebdash cleanup` |

## Building from source

Go 1.27+, no CGO:

```bash
git clone https://github.com/full-bars/URWebDash.git && cd URWebDash
go build -o urwebdash .
```

## License

See repository. Contributions welcome, open an issue first for bigger changes.
