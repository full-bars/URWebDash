# v0.0.23

### Fixed

- **Removed unused unclaimed-alpha card:** The legacy payouts page no longer shows a placeholder that never had data behind it; the Bittensor page keeps its own alpha card.
- **Renamed payouts section:** Now labelled "Legacy Payouts" so it is clear these are the archived USDC-on-Solana payments, not current earnings.

# v0.0.22

### Fixed

- **Removed remaining hardcoded dashboard snapshot values:** Cards now reflect live data.
- **Payout points:** The payout table shows the points earned for each payment.
- **Epoch alpha:** The epoch history in every theme shows each epoch's alpha once it is reported.
- **Payout total subtitle:** The payment count matches the Completed card.

# v0.0.21

### Features

- **Live block progression and dynamic theme updates:** Block progression (blocks done, remaining blocks, and epoch percentage) advances live on every 12-second chain block interval without requiring page refreshes.
- **Real-time dashboard poller (`/api/dash-data`):** Added a lightweight JSON endpoint returning live epoch progress, wallet balances, all-time points, alpha status, and traffic metrics. The frontend polls every 15 seconds to sync open tabs automatically.
- **Configurable webhook alert layouts:** Webhook format styles selected in the UI are now persisted to `~/.urwebdash/webhook_style` via `GET/POST /api/webhook-style`. Both live traffic spike alerts and sample test notifications format and deliver in the chosen layout.
- **Dynamic price trend percentage:** The percentage change badge on the Bittensor price chart recalculates dynamically based on the active timeframe (1H, 24H, 7D, 30D) rather than remaining fixed to the 24-hour quote.
- **Mexican Peso currency support (`MXN`):** Added `MXN` (`Mex$`) to the currency conversion dropdown, backed by live CoinGecko FX rate resolution.

### Fixed

- **Removed hardcoded data points snapshot:** Replaced the hardcoded data points and date range summary header with dynamic row counts, date ranges, and live polling timestamps.
- **Auto-scaling byte units (GB/TB):** Traffic cards, chart axes, tooltips, and labels now scale between GB and TB instead of hardcoding TB, so small amounts no longer show as `0.00 TB`.
- **Dynamic epoch subtitle:** Replaced the static header in the Bittensor section with live epoch and completion values.
- **Synchronized manual refresh:** The manual Refresh button now triggers coordinator epoch polling and dashboard sync alongside traffic stats.

# v0.0.20

### Features

- **Bittensor (SN25) section.** A fourth tab showing the live epoch and its block windows, the epoch history with a points-by-epoch chart, and the alpha price in five currencies.
- **Alpha price with conversion.** The coordinator carries no price, so the quote comes from the GeckoTerminal subnet pool for netuid 25, the same keyless feed ur.xyz resolves against. Conversion (USD, EUR, GBP, JPY, CNY) comes from CoinGecko's TAO quotes. Cached for a minute; a failed refresh keeps the last good quote and says how old it is instead of pretending it is fresh.
- **Epoch earnings recorded.** A new `epoch_stats` table keyed by epoch number, so an open epoch's partial points stay correctable, and one `sn_epoch_state` row for the live geometry. Hourly refresh; two endpoints, `GET /api/epochs` and `POST /api/refresh-epochs`.

### Fixed

- Alpha was rendered 1e9 times too large: the coordinator reports it in rao and it was treated as whole tokens. Scaling now happens once, at the storage boundary.
- A closed epoch could have its points overwritten by a later partial response. Closed epochs are now marked and frozen, including existing rows on upgrade.
- A failed price refresh reported a stale quote as fresh, because the cache reset its timestamp on failure. It now keeps two clocks and reports the true age of the quote and of the exchange rates separately.
- The price and epoch fetches held their locks across network calls, so a slow upstream queued every reader behind it, and a cold start against a dead upstream retried on every request.
- The settlement label ignored the coordinator's own version and could carry an arbitrary string into the page's HTML. Only `v1` or `v2` leaves the server now.
- The page froze its countdowns at load, hard-coded the epoch phase, and showed missing market data as confident zeros.
- A poll failure left no trace, so an empty page claimed it had never polled.

### Internal

- 107 tests before this release; the epoch and price paths add their own, including the rao conversion, the finalized freeze, a failed refresh keeping the real quote age, and the optional conversion path.

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
