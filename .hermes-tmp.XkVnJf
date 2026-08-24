# URWebDash — progress.md

Session handoff notes. Latest state at top. Uncommitted work / pending items flagged.

---

## 2026-08-23/24 session — v0.0.9 release in flight

### Current status

- **v0.0.9 tagged and release workflow running** (run 32677311679, started ~00:38 UTC).
  Binaries + GH release published already; only the **Docker multi-arch push** was
  still in progress (QEMU arm64 build is slow, expect ~5-8 min total). Verify with:
  `gh run list --workflow release --limit 1`
- Release notes for v0.0.9 are in `/tmp/release_notes_09.md` and were applied via
  `gh release edit v0.0.9 --notes-file ...` — CHECK this actually happened after the
  run completes (the auto-generated notes may have been replaced already; if not, re-run
  the edit command).
- After the docker job finishes: `docker pull ghcr.io/full-bars/urwebdash:v0.0.9` should succeed.

### What shipped in v0.0.9 (vs v0.0.7)

1. **Binary renamed `stats_tracker` → `urwebdash`** (module name too). All assets,
   compose commands, Dockerfile, entrypoint updated.
2. **Installer logic moved into the binary**: new `setup.go`, subcommand
   `urwebdash setup [--install-services]`. The curl|bash install.sh is now a thin
   downloader (~40 lines) that execs setup. Auth-code exchange uses Go's encoding/json.
3. **/dev/tty prompt fix**: under `curl | bash`, stdin is the pipe; prompts now fall
   back to /dev/tty so interactive auth code entry WORKS in the pipe case (this was the
   bug our first external user + user themselves hit: "not interactive" dead-end while
   sitting in a terminal).
4. **One-container Docker**: default boot starts poller (background) + dashboard
   (foreground) via entrypoint.sh. Compose collapsed to single service. Fixes Wild
   Clover's empty-charts issue (she had serve-only per old quickstart, no poller → no
   DB rows; payout stats worked because they're fetched live, not from DB).
5. **SQLITE_BUSY first-boot race fixed**: entrypoint sleeps 3s between starting poller
   and serve on brand-new databases.
6. **Env config persists to volume**: DISCORD_WEBHOOK_URL and SPIKE_THRESHOLD env vars
   are written to .urnetwork/discord_webhook and .urnetwork/spike_threshold at startup
   (`syncEnvConfigToVolume()` in main.go), so one-time -e flags become durable.
7. **Port-binding cheat sheet table** added to README Docker section (loopback / LAN /
   tailnet / all-interfaces semantics) after Wild Clover asked how to get LAN access.
8. **uninstall.sh** added: stops/disables services, removes binary, asks before deleting
   data; never deletes a JWT (may belong to a provider install on same machine).

### Key context / decisions made

- **Repo default branch is `master` NOT `main`** — all raw.githubusercontent URLs must
  use /master/. We 404'd install.sh once by assuming main.
- **Release flow**: tag push triggers release.yml → build matrix (4 platforms) → gh
  release → docker buildx multi-arch (amd64 native, arm64 via QEMU) pushed to
  ghcr.io/full-bars/urwebdash with tags latest + vX.Y.Z. Go build caching enabled in
  all setup-go steps (cache: true) — modest gains (~seconds) since project is small;
  kept as hygiene. Docker layer caching via type=gha was already present.
- **Retagging an existing version is messy**: deleting + re-pushing a tag creates a
  SECOND draft release; the notes edit can land on the draft instead of the published
  one. We did it twice for v0.0.7 and had to publish/clean the draft manually.
  Prefer cutting a new version over retagging.
- **CI caching reality check**: cache IS working (verified "Cache restored
  successfully" in logs) but saves only seconds because the project is small. Don't
  oversell it.
- **v0.0.7 image on ghcr has the OLD single-process CMD** (serve only, no poller).
  Anyone running v0.0.7 images needs to pull v0.0.8+ or add explicit command.
- **Webhook storage semantics**: DISCORD_WEBHOOK_URL env var does NOT persist across
  container recreation (env lives in container config, not volume). The file at
  .urnetwork/discord_webhook DOES persist. syncEnvConfigToVolume bridges them:
  env set → file written once → later recreations without -e still work.

### External user support (Wild Clover, Discord DMs via user)

- Deployed docker v0.0.7 serve-only → empty wallet charts (no poller). Fixed by the
  one-container change; gave her a redeploy one-liner (rm + pull + run with port
  3001:3001 for LAN+tailnet access, keeping her existing urwebdash-data volume).
- She used `-e DISCORD_WEBHOOK_URL=...` in her original run → webhook is NOT in her
  volume. Her redeploy command MUST include the env flag one last time (v0.0.8+ will
  persist it to the volume automatically at first start, after which the flag can be
  dropped). Alternatively `docker exec -it urwebdash urwebdash setup`.
- "Error loading wallet stats: no data yet" right after first deploy is normal —
  polls land on quarter-hour marks (:00/:15/:30/:45 UTC).

### Known issues / follow-ups

- **Windows upstream provider logging is terrible**: installer runs `urnetwork provide`
  hidden via Startup shortcut with output discarded. No log file, no Event Log, no
  --log flag anywhere upstream (verified in Provider_Install_Win32.ps1, urnet-tools.ps1,
  provider source). Workaround pattern given to user for their friend:
  Start-Process with -RedirectStandardOutput/Error to files + Get-Content -Wait to tail +
  optional Task Scheduler replacement of the Startup shortcut. Possible future PR to
  upstream connect repo to add file logging.
- **PATH gotcha on Windows/WSL fresh installs**: after install.sh adds ~/.local/bin to
  PATH, the CURRENT shell doesn't see it until restart. Installer output mentions
  restarting the terminal; consider printing the export line directly.
- **release.yml pushes `latest` tag on ANY v* tag including pre-releases** — fine for
  now, would clobber latest stable if we ever cut an -rc.
- **PR #17 merged** (docs/setup-instructions → master, merge commit not squash).
  PR #18 closed in favor of direct-to-master commits (user prefers direct commits for
  doc tweaks now).
- **CodeRabbit round 2 findings**: all addressed in de92958 (JSON parsing hardening,
  generic auth errors, sudo path consistency, README fixes). Round 3+ reviews happen
  automatically per push but free tier = 1 review/hour.

### Repo layout quick reference

- main.go — everything: polling, HTTP server, API clients, notify store, spike logic
- setup.go — urwebdash setup subcommand (token/webhook/services)
- index.html — embedded dashboard UI
- install.sh — thin downloader → exec urwebdash setup
- uninstall.sh — services + binary removal, data opt-in
- docker/entrypoint.sh — JWT bootstrap (existing token > /host-jwt mount > auth code),
  then starts poller bg + serve fg
- deploy/*.service — systemd units (system-level, User=/ExecStart= filled by installer)
- example.env — documented env template
- releases/RELEASE_NOTES.md — OLD detailed notes v0.0.2-v0.0.4 (pre-CHANGELOG)
- CHANGELOG.md — backfilled v0.0.2→v0.0.7, keep updating

### Testing checklist that caught real bugs (rerun before releases)

1. Fresh volume + valid auth code → exchange succeeds, jwt 0600, dashboard 200,
   BOTH processes visible in ps (poller AND serve)
2. Wait for quarter-hour → "[stats] stored:" appears, /api/wallet-stats returns row
3. Restart without env vars → "[entrypoint] using existing JWT", still works
4. Invalid auth code → clean error, exit != hang
5. Root-created bind-mount dir → chown works, no permission denied
6. bash -n install.sh uninstall.sh && sh -n docker/entrypoint.sh
7. go vet ./... && go test ./... && gofmt -l . (empty)
