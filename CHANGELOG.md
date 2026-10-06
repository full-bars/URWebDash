# Changelog

All notable changes. Format loosely follows Keep a Changelog.

## v0.0.17 - 2026-10-06

### Fixed

- Poller could stop recording samples entirely after a reboot. The loop only stored a row when the wall-clock minute was exactly a quarter hour, so a tick that landed a moment before the boundary (a clock adjustment right after boot is enough) was dropped silently — and because the ticker kept the same off-phase, every later poll was dropped too while the process stayed up and healthy. Samples are now keyed on the interval window instead of the wall-clock minute, each sample is stamped at its window boundary, and the boundary is recomputed every iteration (with a catch-up poll if a fetch spans a boundary) so the poller self-heals instead of stalling until it is restarted.
- `cleanupDB` and manual `/api/refresh` now use the same interval-window model as the poller, so neither drops a valid sample nor duplicates a window.

## v0.0.16 - 2026-09-26

### Fixed

- Auto-salvage of lost dashboard history after the v0.0.14 state-dir move. That release relocated the stats database from `/data/wallet_stats.db` to `/data/.urwebdash/wallet_stats.db` in Docker (and `~/.urnetwork` → `~/.urwebdash` natively) but never moved the actual database file, so upgraded installs that had polled for a while kept reading a fresh empty DB at the new path and their history appeared "gone" (the file was still at the old path, untouched). On startup the dashboard now finds legacy DBs in the old locations and merges their rows into the live DB (deduplicated on the unique `created_at`, so it is a true union), preserving any fresh rows the upgraded poller already wrote. Idempotent via a sentinel so it only runs once; provider files are never touched. A completed merge is logged as `[config] recovered N stats rows`.

### Changed

- Traffic spike detection now orders the "previous row" lookup by `created_at` rather than `id`, so backfilled historical rows can't be mistaken for the most recent poll window.

## v0.0.15 - 2026-09-26

### Fixed

- Dashboard content now fills the available window width on wide/ultrawide displays. The main column was capped at 1280px, which left a large empty gutter on the right at 1440px and wider. The webhook panel cap was raised from 720px to 900px.

## v0.0.14 - 2026-09-25

### Changed

- Dashboard state moved to a dedicated `~/.urwebdash` directory (webhook, spike threshold, payout store, sqlite db) — no longer written into the provider's `~/.urnetwork`. The provider JWT is still read read-only from `~/.urnetwork/jwt`.
- First run of v0.0.14 auto-migrates existing dashboard files out of `~/.urnetwork` into `~/.urwebdash` (provider files are never touched).
- Docker: dashboard state now under `/data/.urwebdash` (was `/data/.urnetwork`), persisted in the same volume.
- New `URWEBDASH_HOME` env var overrides the state directory.

## v0.0.13 - 2026-09-25

### Added

- Dashboard traffic-spike threshold editor: set the spike alert threshold in GB from the Webhook section (decimal stepper, default 1.0). Saves to `~/.urnetwork/spike_threshold` (0600) and takes effect immediately; a `SPIKE_THRESHOLD` env var still wins until removed.

## v0.0.12 - 2026-09-25

### Added

- Dashboard webhook management: new "🔔 Webhook" section where you can view the configured state (URL shown masked), paste a Discord webhook URL, save it, send a test notification, or clear it. Saves to `~/.urnetwork/discord_webhook` (Docker: `/data/.urnetwork/discord_webhook`, persisted in the volume) and takes effect immediately — no restart.
- API endpoints backing the section: `GET`/`POST /api/webhook` (responses mask the secret token) and `POST /api/webhook-test` (returns Discord's actual response or success; tests the URL in the input box before saving).
- Installer auto-removes the legacy `stats_tracker` binary on upgrade.

### Changed

- `DISCORD_WEBHOOK_URL` env var still takes precedence over the URL saved from the dashboard; the UI shows a warning so the overlap is visible.
- Notification posting refactored around a shared synchronous helper (no behavior change to the poller or `urwebdash testwebhook`).
- Webhook `POST` endpoints require `Content-Type: application/json` and reject foreign `Origin` headers (CSRF protection); failed test sends report a generic error instead of echoing the webhook URL/token.

### Docs

- README: generic LAN IP example in the Docker section.

## v0.0.11 - 2026-08-23

### Changed

- First-run UX: dashboard polls immediately at startup (charts populate without waiting for the next quarter-hour), clearer startup banner, no foreground/background confusion.
- README restructured — "System services" promoted to its own section; immediate-first-poll and uninstall guidance added.

### Internal

- Removed stray `.hermes-tmp` files.

## v0.0.10 - 2026-08-23

### Added

- `setup` auto-configures the shell PATH (bash/zsh/fish aware, idempotent) and auto-starts the poller + dashboard when it finishes.

### Changed

- Sudo hints print the absolute resolved binary path (`secure_path` never includes `~/.local/bin`).

## v0.0.9 - 2026-08-23

### Added

- `uninstall.sh` — stops/disables services, removes the binary, and asks before deleting data.

### Changed

- Interactive setup prompts via `/dev/tty` so `curl | bash` stays interactive.

## v0.0.8 - 2026-08-23

### Added

- Env-provided webhook/spike settings persist to the data volume: `DISCORD_WEBHOOK_URL` and `SPIKE_THRESHOLD` set via `-e` are written to `~/.urnetwork/*` on startup so they survive container recreation.

## v0.0.7 - 2026-08-23

### Security

- Dashboard binds to `127.0.0.1` by default. Previous builds listened on all interfaces, exposing wallet data to anyone scanning the host. Set `HOST` to override.

### Added

- One-shot installer (`install.sh`): downloads the binary, sets up a session token (prompts for an auth code only if none exists, exchanged via the bringyour.com code-login API), and installs systemd services when run as root.
- Docker support: multi-arch image (amd64/arm64), compose stack with poller + dashboard services, and JWT bootstrap via `URNETWORK_AUTH_CODE`. Container starts as root only to fix bind-mount ownership, then drops to an unprivileged user (`PUID`/`PGID`, default 1000).
- Configurable traffic-spike threshold: `SPIKE_THRESHOLD` accepts human sizes (`500M`, `0.5G`, `1.5GB`) via env var or `~/.urnetwork/spike_threshold`.
- Installer sets up Discord webhook alerts interactively (skipped when non-interactive).
- Release workflow publishes binaries and multi-arch container images with Go build caching.

### Changed

- Docker image default command is now the dashboard (`serve`); the poller is explicit (`urwebdash run`).

### Fixed

- Traffic-spike gap guard compares unsigned values to prevent int64 wraparound firing spurious alerts.
- Entrypoint no longer forks recursively when parsing the auth-code response.
- Auth code never appears in process arguments during exchange.

## v0.0.6

Maintenance release. CI hardening: least-privilege workflow permissions, release-parity build checks, cross-compile smoke tests, Docker layer caching.

## v0.0.5

Maintenance release.

## v0.0.4 - see [release](https://github.com/full-bars/URWebDash/releases/tag/v0.0.4)

- Last 7 days usage strip; estimated pending payouts; dynamic network name from JWT
- Payout notifications survive restarts (persisted dedup store)
- Payout "Last Updated" timestamp fixed on lazy refresh path

## v0.0.3 - see [release](https://github.com/full-bars/URWebDash/releases/tag/v0.0.3)

- Rate chart downsampling fix; auto-refresh no longer flashes charts
- Sidebar renamed to "URnetwork Fleet Data & Payouts"
- Removed "Clear History" button

## v0.0.2 - initial public release
