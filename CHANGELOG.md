# Changelog

All notable changes. Format loosely follows Keep a Changelog.

## Unreleased

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
