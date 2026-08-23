# URWebDash

Self-hosted wallet and payout stats dashboard for [URnetwork](https://ur.network) providers. Polls the bringyour.com API, stores history in local SQLite, and serves a dark-theme Chart.js UI.

![dashboard screenshot](https://i.postimg.cc/d3wcVFsg/image.png)

**Features**

- Paid/unpaid bytes tracking with hourly snapshots and transfer-rate charts
- Last-7-days usage strip (calendar-day buckets, DST-safe)
- Full payment history: amounts, points per payment, Solana tx links
- Estimated amounts for pending payouts
- Optional Discord notifications on new/completed payments
- Auto-refresh: dashboard every 30s, payout stats every 5m

---

## Quick start (Linux / macOS)

No Go toolchain needed:

```bash
curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/main/install.sh | bash
```

That installs the latest prebuilt binary to `~/.local/bin/stats_tracker`.

URWebDash reads your URnetwork JWT from `~/.urnetwork/jwt`:

```bash
ls -l ~/.urnetwork/jwt
```

- **Same machine as your URnetwork provider install?** The file is already there — done.
- **Different machine?** Copy it over:
  ```bash
  mkdir -p ~/.urnetwork
  scp your-provider-machine:.urnetwork/jwt ~/.urnetwork/jwt
  ```

Try it out:

```bash
~/.local/bin/stats_tracker run &     # polling daemon, fetches every 15 min
~/.local/bin/stats_tracker serve     # dashboard on http://localhost:3001
```

Open **http://localhost:3001**.

The dashboard listens on `127.0.0.1` only (loopback). For remote access, put a
reverse proxy or Cloudflare Tunnel in front rather than exposing the port.

Working? Make it permanent: [Run as a service](#run-as-a-service-systemd).

---

## Run as a service (systemd)

Two small units: one polls for stats, one serves the web UI.

```bash
sudo cp deploy/urwebdash-run.service deploy/urwebdash-serve.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now urwebdash-run.service urwebdash-serve.service
```

Check status and logs:

```bash
systemctl status urwebdash-run urwebdash-serve --no-pager
journalctl -u urwebdash-serve -f
```

> The unit files assume the binary lives at `/home/YOUR_USER/.local/bin/stats_tracker`. Edit `User=` and `ExecStart=` to match your setup, or just run the installer as root — it fills these in for you.

---

## Run with Docker

Prebuilt multi-arch image (amd64 + arm64), published on release:

```bash
mkdir urwebdash && cd urwebdash
curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/main/example.env -o .env
curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/main/docker-compose.yml -o docker-compose.yml
```

Edit `.env` (every line is optional and commented) and pick **one** of these JWT options:

1. **Auth code** — get one at https://ur.io, set `URNETWORK_AUTH_CODE` in `.env`. Exchanged for a session token on first start and saved to `./data/jwt`; you can clear the variable afterwards.
2. **Existing jwt file** — uncomment the `~/.urnetwork/jwt:/host-jwt:ro` mount in `docker-compose.yml`. Copied into the data volume once; the host file is never written.

Start both containers:

```bash
docker compose up -d
docker compose logs -f   # watch the entrypoint do its thing
```

Dashboard on http://127.0.0.1:3001 (loopback only by default - edit the `ports:` mapping for anything else). All state lives in `./data`: database, session token, webhook config, notification dedup store. Back up that directory and you have backed up everything.

To run only the poller or only the dashboard: `docker compose up -d run` / `docker compose up -d serve`.

Environment variables are read from `.env` (compose) or set directly with `-e` (`docker run`). See [Configuration](#configuration) for the full list.

---

## Building from source

Only needed if there's no binary for your platform or you want to hack on it.

Requirements: **Go 1.26+** (no CGO required — pure-Go SQLite driver).

```bash
git clone https://github.com/full-bars/URWebDash.git
cd URWebDash
go build -o stats_tracker .
```

---

## Commands

| Command | Description |
|---|---|
| `run` | Polling daemon — fetches `/account/stats` every 15m on quarter-hour marks, stores in SQLite |
| `serve [port]` | HTTP server — dashboard on port 3001 (default) |
| `import <file>` | Import historical wallet-stats records from a JSON export (array of records with `paid_bytes_provided`, `unpaid_bytes`, `created_at`, `updated_at`; also accepts a `{"content": "..."}` wrapper) |
| `history` | Print all stored records to stdout |
| `cleanup` | Delete off-schedule `wallet_stats` entries for today (keeps quarter-hour rows) |
| `testwebhook` | Send a sample Discord notification to verify `DISCORD_WEBHOOK_URL` |

---

## Configuration

All optional. Environment variables only.

| Variable | Default | Description |
|---|---|---|
| `STATS_INTERVAL` | `15m` | Polling interval (minimum 1m) |
| `JWT_PATH` | `~/.urnetwork/jwt` | Path to the URnetwork JWT token file |
| `STATS_DB` | `~/.urnetwork/wallet_stats.db` | SQLite database path |
| `DISCORD_WEBHOOK_URL` | *(unset)* | Discord webhook URL for payout notifications (or `~/.urnetwork/discord_webhook` file) |
| `SPIKE_THRESHOLD` | `1GB` | Per-window unpaid-bytes delta that triggers a traffic-spike alert. Human sizes accepted: `500MB`, `500M`, `0.5G`, `1.5GB` |
| `PAYOUT_NOTIFY_STORE` | `~/.urnetwork/payout_notified.json` | Notification dedup store (survives restarts) |

To set them for the systemd services, add an override:

```bash
sudo systemctl edit urwebdash-run
```

```ini
[Service]
Environment=STATS_INTERVAL=5m
Environment=STATS_DB=/var/lib/urwebdash/wallet_stats.db
```

then `sudo systemctl restart urwebdash-run`.

---

## Discord notifications

1. In Discord: **Server Settings → Integrations → Webhooks → New Webhook**, pick a channel, copy the URL.
2. Point the daemon at it and send yourself a test:

   ```bash
   export DISCORD_WEBHOOK_URL="https://discord.com/api/webhooks/..."
   ~/.local/bin/stats_tracker testwebhook
   ```

3. Add it to the service override (`sudo systemctl edit urwebdash-run`) as above and restart.

You'll get a ping when a new payment appears, when a pending payment completes, and when traffic crosses your spike threshold within one polling window.

The installer asks for the webhook URL (blank to skip) and an optional spike threshold, so this section is only for manual setups.

---

## Security notes

- The dashboard has **no built-in authentication**. It binds to `127.0.0.1` by default so only local processes can reach it; set `HOST=0.0.0.0` to change that (not recommended).
- For remote access, use a reverse proxy with auth (nginx + basic auth, Cloudflare Access, Tailscale, etc.).
- Don't commit or share your JWT — it grants full access to your account API.

---

## Updating

Prebuilt install:

```bash
curl -fsSL https://raw.githubusercontent.com/full-bars/URWebDash/main/install.sh | bash
sudo systemctl restart urwebdash-run urwebdash-serve
```

Your SQLite data lives separately (`~/.urnetwork/wallet_stats.db`) and survives updates untouched.

---

## Uninstall

```bash
sudo systemctl disable --now urwebdash-run urwebdash-serve
sudo rm /etc/systemd/system/urwebdash-{run,serve}.service
rm -f ~/.local/bin/stats_tracker
# optional: delete data
rm -rf ~/.urnetwork/wallet_stats.db ~/.urnetwork/payout_notified.json
```

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `read jwt: no such file or directory` | No JWT at `~/.urnetwork/jwt`. Copy one from your provider machine (see Quick start) or set `JWT_PATH`. |
| Dashboard shows empty charts | Polling hasn't run yet — wait for the next quarter-hour mark, or lower `STATS_INTERVAL`. Check `journalctl -u urwebdash-run`. |
| API errors after a while | Your JWT likely expired/was rotated. Re-copy a fresh `jwt` file and restart the services. |
| Port 3001 already in use | Start with another port: `stats_tracker serve 3002` (and update the tunnel/unit file). |
| No Discord notifications | Run `stats_tracker testwebhook` with `DISCORD_WEBHOOK_URL` set; check the dedup store at `PAYOUT_NOTIFY_STORE` if testing repeatedly. |
| Duplicate/off-schedule rows today | Run `stats_tracker cleanup`. |

---

## License

See repository. Contributions welcome — open an issue first for bigger changes.
