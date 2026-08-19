# v0.0.4

### Features

- **Last 7 Days Usage Strip** - Added a per-day usage card strip under the summary cards, computed client-side from existing byte-change deltas. Buckets use local calendar-day arithmetic so they stay aligned to midnight across DST transitions.
- **Estimated Pending Payouts** - Pending payments now show an estimated dollar amount instead of an em dash. The API already returns the gross booked amount for pending payments, so the estimate fills in the unknown wallet fee using the median gap between booked and settled amounts across the 10 most recent settled payments. Adds an "Estimated Pending" stat card; Total Earned now renders to 4 decimals to match.
- **Dynamic Network Name** - The sidebar network label is now decoded from the JWT via a new `/api/network` endpoint instead of showing a hardcoded placeholder.

### Fixes

- **Payout Notifications Survive Restarts** - Notification dedup state was tracked only in memory, so restarting the process re-announced every historical payout as new. The baseline is now persisted to `~/.urnetwork/payout_notified.json` (atomic writes, keyed by payment ID) and seeded silently on cold start. Also hardened against bad cold-start baselines — a transient empty fetch or empty store file no longer poisons the dedup state — and removed an unreachable dead branch left over from the completion-flip logic.
- **Payout "Last Updated" Timestamp** - The lazy cache-refresh path now populates `last_update` (previously only the explicit refresh path did), fixing a stuck "Last updated: —" on the Payout Statistics page.

### Internal

- **Weekly Strip Hardening** - Clamped negative deltas to 0, guarded against a missing wallet-entries global, and skip malformed timestamps in the new usage strip.
- **gofmt Cleanliness** - Restored formatting on `main.go` (whitespace/alignment only, no behavior change).

# v0.0.3

### UI

- **Sidebar Title** – "URnetwork Client Manager" → "URnetwork Fleet Data & Payouts"

### Fixes

- **1H Rate Chart** - Fixed downsampling bug where index-based sampling skipped 9/10 entries (~150min spacing), causing only 1 bar to appear. Rate data now uses all entries for accurate deltas.
- **Chart Glitch on Auto-Refresh** - Rate chart was destroying and recreating itself every 30 seconds, causing a visible flash. Now updates data in-place.
- **44h Change on Payout Page** - No longer goes negative on the payout tab.
- **Removed "Clear History"** - Removed the "Clear History" button and its API handler to prevent accidental data loss.

### Internal

- **CI Improvements** - Added `-race` detection, `gofmt` formatting check, and a wallet-stats response validation test

# v0.0.2

### Features

- **Payout Points per Payment** - Two columns in the payout table: total points earned and reliability points per payment
- **Discord Webhook Notifications** - Automatic alerts when a new payment arrives or a pending payment completes
- **Version + Uptime in Sidebar** - Footer shows build version and service uptime

### Fixes

- **24h Change on Wallet Stats** - Now displays actual data volume change over the last 24 hours
- **Format String Bug** - Fixed format string issue in payout display

### Internal

- **Resilient Polling** - Retries API fetch with backoff on missed poll windows
- **Database Cleanup** - Removed `.db` files from git tracking
- **Test Suite** - 22 tests covering API fetch, JWT, payout cache, import, and refresh handlers
- **CI Pipeline** - `go vet` and `go test` on every PR/push to master

# v0.0.1

- **Fix:** Auto-refresh no longer resets transfer rate time window (uses `rateWindow` instead of hardcoded `'1h'`)
- **Perf (backend):** Payout cache uses double-check RLock — HTTP fetch no longer blocks readers for 15s
- **Perf (backend):** Shared `http.Client` and cached JWT token
- **Perf (backend):** DB connection pool tuning (max open/idle/conn lifetime)
- **Perf (frontend):** Paid/unpaid charts use `Chart.update()` instead of destroy+create
- **Perf (frontend):** Payouts lazy-loaded only when payout tab is active
- **Accuracy:** Rate data computed from raw entries (2K cap) instead of downsampled chartSlice
