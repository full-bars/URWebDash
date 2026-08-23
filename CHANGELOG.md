# Changelog

All notable changes. Format loosely follows Keep a Changelog.

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

- Docker image default command is now the dashboard (`serve`); the poller is explicit (`stats_tracker run`).

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
