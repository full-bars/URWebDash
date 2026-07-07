# v0.0.1

- **Fix:** Auto-refresh no longer resets transfer rate time window (uses `rateWindow` instead of hardcoded `'1h'`)
- **Perf (backend):** Payout cache uses double-check RLock — HTTP fetch no longer blocks readers for 15s
- **Perf (backend):** Shared `http.Client` and cached JWT token
- **Perf (backend):** DB connection pool tuning (max open/idle/conn lifetime)
- **Perf (frontend):** Paid/unpaid charts use `Chart.update()` instead of destroy+create
- **Perf (frontend):** Payouts lazy-loaded only when payout tab is active
- **Accuracy:** Rate data computed from raw entries (2K cap) instead of downsampled chartSlice
