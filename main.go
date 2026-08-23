package main

import (
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed index.html
var indexHTML string

type walletStats struct {
	PaidBytes   uint64 `json:"paid_bytes_provided"`
	UnpaidBytes uint64 `json:"unpaid_bytes_provided"`
	Error       *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type exportRecord struct {
	ID                string `json:"id"`
	UserID            string `json:"user_id"`
	NetworkName       string `json:"network_name"`
	PaidBytesProvided uint64 `json:"paid_bytes_provided"`
	UnpaidBytes       uint64 `json:"unpaid_bytes"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type payoutRecord struct {
	PaymentID       string   `json:"payment_id"`
	TokenAmount     float64  `json:"token_amount"`
	PayoutByteCount int64    `json:"payout_byte_count"`
	PayoutNanoCents float64  `json:"payout_nano_cents"`
	PointsEarned    float64  `json:"points_earned"`
	ReliabilityPts  float64  `json:"reliability_points"`
	Completed       bool     `json:"completed"`
	Canceled        bool     `json:"canceled"`
	CreateTime      string   `json:"create_time"`
	CompleteTime    string   `json:"complete_time"`
	PaymentTime     string   `json:"payment_time"`
	TxHash          string   `json:"tx_hash"`
	WalletAddress   string   `json:"wallet_address"`
	Blockchain      string   `json:"blockchain"`
	TokenType       string   `json:"token_type"`
	EstimatedAmount *float64 `json:"estimated_amount,omitempty"`
}

type accountResponse struct {
	AccountPayments []payoutRecord `json:"account_payments"`
	AccountPoints   []interface{}  `json:"account_points"`
	Error           *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type pointEntry struct {
	PointValue       int64  `json:"point_value"`
	AccountPaymentID string `json:"account_payment_id"`
	Event            string `json:"event"`
}

type pointsResponse struct {
	AccountPoints []pointEntry `json:"account_points"`
	Error         *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

var (
	Version   = "dev"
	startTime = time.Now()

	payoutCache      []payoutRecord
	payoutCacheMu    sync.RWMutex
	payoutCacheTime  time.Time
	payoutLastError  string
	payoutLastUpdate string
	payoutLastPoints float64

	httpClient = &http.Client{Timeout: 15 * time.Second}
	cachedJWT  string

	// Payout notification dedup state, persisted to disk so a process
	// restart does not re-announce every historical payout as new.
	notifyStore       map[string]notifyRecord
	notifyStoreMu     sync.Mutex
	notifyStorePath   string
	notifyStoreSeeded bool
	notifySend        = sendDiscordNotification // test hook
)

type notifyRecord struct {
	TxHash    string `json:"tx_hash"`
	Completed bool   `json:"completed"`
}

func main() {
	flag.Parse()
	cmd := flag.Arg(0)

	switch cmd {
	case "run":
		runPolling()
	case "serve":
		serveHTTP(flag.Arg(1))
	case "import":
		importJSON(flag.Arg(1))
	case "history":
		printHistory()
	case "cleanup":
		cleanupDB()
	case "testwebhook":
		url := discordWebhookURL()
		if url == "" {
			fmt.Fprintln(os.Stderr, "no webhook URL configured")
			os.Exit(1)
		}
		body, _ := json.Marshal(map[string]string{"content": "🛫 **Traffic Spike**\n```\n┌──────────────────────┬────────────┐\n├──────────────────────┼────────────┤\n│ 15m Delta            │   +1.45 GB │\n│ Total Unpaid         │  742.29 GB │\n│ At (UTC)             │   01:15:01 │\n└──────────────────────┴────────────┘\n```"})
		resp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "POST error: %v\n", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode > 299 {
			b, _ := io.ReadAll(resp.Body)
			fmt.Fprintf(os.Stderr, "%d: %s\n", resp.StatusCode, string(b))
			os.Exit(1)
		}
		fmt.Println("Test notification sent successfully")
	default:
		fmt.Println(`Usage:
  stats_tracker run                    — start polling daemon
  stats_tracker serve [port]          — start HTTP server (default :3001)
  stats_tracker import <file.json>     — import wallet stats history from a JSON export
  stats_tracker history                — print stored history
  stats_tracker cleanup                — delete off-schedule wallet_stats entries for today
  stats_tracker testwebhook           — send a test Discord notification`)
	}
}

func dbPath() string {
	if p := os.Getenv("STATS_DB"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".urnetwork", "wallet_stats.db")
}

func dbDSN() string {
	// Apply busy_timeout on EVERY pooled connection. A db.Exec("PRAGMA
	// busy_timeout") only configures whichever connection happened to run
	// it; the pool can open others that still return SQLITE_BUSY. The
	// _pragma DSN parameter is applied to each newly opened connection.
	// journal_mode=WAL is NOT put here: it is a persistent file property
	// stored in the database header, so the init loop sets it once.
	//
	// Build the path as a percent-encoded file: URI so a literal '?' or '#'
	// in STATS_DB cannot split the DSN and silently drop the _pragma.
	u := url.URL{Scheme: "file", Path: dbPath()}
	q := url.Values{}
	q.Set("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	return u.String()
}

func jwtPath() string {
	if p := os.Getenv("JWT_PATH"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".urnetwork", "jwt")
}

func openDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbDSN())
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	for _, stmt := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		`CREATE TABLE IF NOT EXISTS wallet_stats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL DEFAULT '',
			network_name TEXT NOT NULL DEFAULT '',
			paid_bytes INTEGER NOT NULL DEFAULT 0,
			unpaid_bytes INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL UNIQUE,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_stats_time ON wallet_stats(created_at)`,
		`CREATE TABLE IF NOT EXISTS payout_stats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			token_amount REAL NOT NULL DEFAULT 0,
			payout_byte_count INTEGER NOT NULL DEFAULT 0,
			completed INTEGER NOT NULL DEFAULT 0,
			canceled INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_payout_time ON payout_stats(created_at)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init: %w", err)
		}
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

func readJWT() (string, error) {
	if cachedJWT != "" {
		return cachedJWT, nil
	}
	b, err := os.ReadFile(jwtPath())
	if err != nil {
		return "", fmt.Errorf("read jwt: %w", err)
	}
	cachedJWT = strings.TrimSpace(string(b))
	return cachedJWT, nil
}

func networkNameFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		NetworkName string `json:"network_name"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.NetworkName
}

func fetchStats(token string) (*walletStats, error) {
	req, err := http.NewRequest("GET", "https://api.bringyour.com/transfer/stats", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "*/*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API %d: %s", resp.StatusCode, string(body))
	}

	var s walletStats
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &s, nil
}

func fetchPayouts(token string) (*accountResponse, error) {
	req, err := http.NewRequest("GET", "https://api.bringyour.com/account/payments", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "*/*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API %d: %s", resp.StatusCode, string(body))
	}

	var r accountResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &r, nil
}

func fetchPoints(token string) ([]pointEntry, error) {
	req, err := http.NewRequest("GET", "https://api.bringyour.com/account/points", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "*/*")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API %d: %s", resp.StatusCode, string(body))
	}

	var r pointsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	return r.AccountPoints, nil
}

// settlementFee estimates the typical wallet/transfer fee applied when a
// payment settles: the median gap between the gross amount the network booked
// (payout_nano_cents) and the tokens that actually landed (token_amount),
// sampled over the most recent settled payments. Only recent payments are
// sampled because the fee has changed over time; the median keeps a one-off
// outlier from skewing the estimate.
func settlementFee(payments []payoutRecord) float64 {
	const feeSampleSize = 10

	type sample struct {
		t   time.Time
		fee float64
	}
	var samples []sample
	for _, p := range payments {
		if p.TokenAmount <= 0 || p.PayoutNanoCents <= 0 || p.PaymentTime == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, p.PaymentTime)
		if err != nil {
			continue
		}
		samples = append(samples, sample{t: t, fee: p.PayoutNanoCents/1e9 - p.TokenAmount})
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].t.After(samples[j].t) })
	if len(samples) > feeSampleSize {
		samples = samples[:feeSampleSize]
	}

	var fees []float64
	for _, s := range samples {
		if s.fee >= 0 {
			fees = append(fees, s.fee)
		}
	}
	if len(fees) == 0 {
		return 0
	}
	sort.Float64s(fees)
	return fees[len(fees)/2]
}

// estimateFor returns the estimated settlement amount for a payment that has
// not been paid yet: the gross booked amount minus the typical fee. Returns
// nil when the payment already has a token amount or no booked amount exists.
func estimateFor(p payoutRecord, fee float64) *float64 {
	if p.TokenAmount > 0 || p.PayoutNanoCents <= 0 {
		return nil
	}
	est := p.PayoutNanoCents/1e9 - fee
	if est < 0 {
		est = 0
	}
	return &est
}

// enrichPayoutEstimates stamps each unpaid payment with its estimated amount
// and returns the total estimated value of all pending payments.
func enrichPayoutEstimates(payments []payoutRecord) float64 {
	fee := settlementFee(payments)
	var total float64
	for i := range payments {
		if est := estimateFor(payments[i], fee); est != nil {
			payments[i].EstimatedAmount = est
			total += *est
		}
	}
	return total
}

func runPolling() {
	token, err := readJWT()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	interval := statsInterval()

	fmt.Printf("[stats] polling every %s | db=%s\n", interval, dbPath())

	update := func() {
		now := time.Now().UTC()
		_, min, _ := now.Clock()
		if min%15 != 0 {
			return
		}

		windowStart := now.Truncate(15 * time.Minute)
		var existingID int64
		db.QueryRow("SELECT id FROM wallet_stats WHERE created_at >= ? AND created_at < ? LIMIT 1",
			windowStart.Format(time.RFC3339),
			windowStart.Add(15*time.Minute).Format(time.RFC3339),
		).Scan(&existingID)
		if existingID != 0 {
			return
		}

		var stats *walletStats
		var err error
		for attempt := range 3 {
			stats, err = fetchStats(token)
			if err == nil {
				break
			}
			fmt.Printf("[stats] fetch error (attempt %d/3): %v\n", attempt+1, err)
			if attempt < 2 {
				time.Sleep(time.Duration(attempt+1) * 5 * time.Second)
			}
		}
		if err != nil {
			fmt.Printf("[stats] all 3 attempts failed for window %s\n", windowStart.Format("15:04"))
			return
		}

		nowStr := now.Format(time.RFC3339)
		_, err = db.Exec(
			"INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?, ?, ?, ?)",
			int64(stats.PaidBytes), int64(stats.UnpaidBytes), nowStr, nowStr,
		)
		if err != nil {
			fmt.Printf("[stats] insert error: %v\n", err)
			return
		}
		fmt.Printf("[stats] stored: paid=%d unpaid=%d\n", stats.PaidBytes, stats.UnpaidBytes)

		checkTrafficSpike(db, stats.UnpaidBytes, now)
	}

	// Align to next quarter-hour boundary
	now := time.Now()
	_, min, _ := now.Clock()
	nextQ := ((min / 15) + 1) * 15
	firstTick := now.Truncate(time.Hour).Add(time.Duration(nextQ) * time.Minute)
	if firstTick.Before(now) {
		firstTick = firstTick.Add(interval)
	}
	sleepDur := firstTick.Sub(now)
	fmt.Printf("[stats] next poll at %s (in %v)\n", firstTick.Format("15:04:05"), sleepDur.Round(time.Second))
	time.Sleep(sleepDur)
	update()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		update()
	}
}

func importJSON(path string) {
	if path == "" {
		fmt.Fprintln(os.Stderr, "usage: stats_tracker import <file.json>")
		os.Exit(1)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}

	var records []exportRecord
	if err := json.Unmarshal(data, &records); err != nil {
		var wrapper struct {
			Content string `json:"content"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil || wrapper.Content == "" {
			fmt.Fprintf(os.Stderr, "parse: %v\n", err)
			os.Exit(1)
		}
		if err := json.Unmarshal([]byte(wrapper.Content), &records); err != nil {
			fmt.Fprintf(os.Stderr, "parse wrapped: %v\n", err)
			os.Exit(1)
		}
	}

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt < records[j].CreatedAt
	})

	imported := 0
	for _, r := range records {
		res, err := db.Exec(
			"INSERT OR IGNORE INTO wallet_stats(user_id, network_name, paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)",
			r.UserID, r.NetworkName, int64(r.PaidBytesProvided), int64(r.UnpaidBytes), r.CreatedAt, r.UpdatedAt,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "insert %s: %v\n", r.CreatedAt, err)
			continue
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			imported++
		}
	}
	fmt.Printf("imported %d records\n", imported)
}

func printHistory() {
	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	rows, err := db.Query("SELECT id, user_id, network_name, paid_bytes, unpaid_bytes, created_at FROM wallet_stats ORDER BY created_at ASC")
	if err != nil {
		fmt.Fprintf(os.Stderr, "query: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	fmt.Printf("%-6s %-24s %12s %12s  %s\n", "ID", "Time (UTC)", "Paid", "Unpaid", "Delta")
	var prevPaid, prevUnpaid int64
	count := 0
	for rows.Next() {
		var id int64
		var uid, net, ts string
		var paid, unpaid int64
		if err := rows.Scan(&id, &uid, &net, &paid, &unpaid, &ts); err != nil {
			continue
		}
		if count > 0 {
			dpaid := paid - prevPaid
			dunpaid := unpaid - prevUnpaid
			fmt.Printf("%-6d %-24s %12d %12d  paid=%+d unpaid=%+d\n", id, ts, paid, unpaid, dpaid, dunpaid)
		} else {
			fmt.Printf("%-6d %-24s %12d %12d  (baseline)\n", id, ts, paid, unpaid)
		}
		prevPaid, prevUnpaid = paid, unpaid
		count++
	}
	fmt.Printf("\n%d entries\n", count)
}

func cleanupDB() {
	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	today := time.Now().UTC().Format("2006-01-02")
	res, err := db.Exec(
		"DELETE FROM wallet_stats WHERE created_at >= ? AND created_at < ? AND (CAST(strftime('%M', created_at) AS INTEGER) % 15 != 0 OR CAST(strftime('%S', created_at) AS INTEGER) > 5)",
		today+"T00:00:00Z",
		today+"T24:00:00Z",
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cleanup: %v\n", err)
		os.Exit(1)
	}
	n, _ := res.RowsAffected()
	fmt.Printf("cleaned %d off-schedule entries from %s\n", n, today)
}

// --- HTTP Server ---

func serveHTTP(port string) {
	if port == "" {
		port = "3001"
	}

	token, err := readJWT()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}

	db, err := openDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/wallet-stats", handleWalletStats(db))
	mux.HandleFunc("/api/wallet-summary", handleWalletSummary(db))
	mux.HandleFunc("/api/payout-stats", handlePayoutStats(token))
	mux.HandleFunc("/api/refresh", handleRefresh(token, db))
	mux.HandleFunc("/api/refresh-payout", handleRefreshPayout(token))
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/network", handleNetworkName(token))
	mux.HandleFunc("/", handleIndex)

	host := os.Getenv("HOST")
	if host == "" {
		// Bind loopback by default. The dashboard is exposed only through the
		// Cloudflare tunnel / local reverse proxy; binding all interfaces would
		// serve wallet/payout data to anyone scanning the box's public IP.
		host = "127.0.0.1"
	}

	addr := net.JoinHostPort(host, port)
	fmt.Printf("[serve] listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "serve error: %v\n", err)
		os.Exit(1)
	}
}

func handleWalletStats(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}

		rows, err := db.Query("SELECT paid_bytes, unpaid_bytes, created_at FROM wallet_stats ORDER BY created_at ASC")
		if err != nil {
			jsonError(w, err.Error())
			return
		}
		defer rows.Close()

		type entry struct {
			PaidBytes   int64  `json:"paid_bytes"`
			UnpaidBytes int64  `json:"unpaid_bytes"`
			CreatedAt   string `json:"created_at"`
			ChangeBytes int64  `json:"change_bytes"`
		}
		var entries []entry
		var prevTotal int64
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.PaidBytes, &e.UnpaidBytes, &e.CreatedAt); err != nil {
				continue
			}
			total := e.PaidBytes + e.UnpaidBytes
			if len(entries) > 0 {
				e.ChangeBytes = total - prevTotal
			}
			prevTotal = total
			entries = append(entries, e)
		}

		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}

		var totalCount int
		db.QueryRow("SELECT COUNT(*) FROM wallet_stats").Scan(&totalCount)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"entries": entries,
			"count":   len(entries),
			"total":   totalCount,
		})
	}
}

func handleWalletSummary(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}

		row := db.QueryRow("SELECT paid_bytes, unpaid_bytes, created_at FROM wallet_stats ORDER BY created_at DESC LIMIT 1")
		var paid, unpaid int64
		var ts string
		err := row.Scan(&paid, &unpaid, &ts)
		if err != nil {
			if err != sql.ErrNoRows {
				fmt.Printf("[wallet-summary] scan error: %v\n", err)
			}
			jsonError(w, "no data yet")
			return
		}

		var count int
		db.QueryRow("SELECT COUNT(*) FROM wallet_stats").Scan(&count)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"paid_bytes":   paid,
			"unpaid_bytes": unpaid,
			"total_bytes":  paid + unpaid,
			"updated_at":   ts,
			"count":        count,
		})
	}
}

func handlePayoutStats(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payoutCacheMu.RLock()
		needsRefresh := len(payoutCache) == 0 || payoutCacheTime.IsZero() || time.Since(payoutCacheTime) > 5*time.Minute
		payoutCacheMu.RUnlock()

		if needsRefresh {
			if resp, err := fetchPayouts(token); err == nil {
				pointEntries, _ := fetchPoints(token)
				pointsByPayment := make(map[string]float64)
				reliabilityByPayment := make(map[string]float64)
				var totalPoints float64
				var totalReliability float64
				for _, pe := range pointEntries {
					pointsByPayment[pe.AccountPaymentID] += float64(pe.PointValue) / 1e6
					totalPoints += float64(pe.PointValue) / 1e6
					if pe.Event == "payout_reliability" {
						reliabilityByPayment[pe.AccountPaymentID] += float64(pe.PointValue) / 1e6
						totalReliability += float64(pe.PointValue) / 1e6
					}
				}
				for i := range resp.AccountPayments {
					resp.AccountPayments[i].PointsEarned = pointsByPayment[resp.AccountPayments[i].PaymentID]
					resp.AccountPayments[i].ReliabilityPts = reliabilityByPayment[resp.AccountPayments[i].PaymentID]
				}
				payoutCacheMu.Lock()
				payoutCache = resp.AccountPayments
				payoutCacheTime = time.Now()
				payoutLastUpdate = time.Now().UTC().Format(time.RFC3339)
				payoutLastError = ""
				payoutLastPoints = totalPoints
				payoutCacheMu.Unlock()

				// Seed the notification baseline on cold start; never
				// announce from the lazy page-load path.
				syncPayoutNotifyStore(resp.AccountPayments, false)
			}
		}

		payoutCacheMu.RLock()
		cached := payoutCache
		lastTime := payoutCacheTime
		lastErr := payoutLastError
		lastUpd := payoutLastUpdate
		pts := payoutLastPoints
		isFresh := !lastTime.IsZero() && time.Since(lastTime) < 5*time.Minute
		payoutCacheMu.RUnlock()

		respPayments := make([]payoutRecord, len(cached))
		copy(respPayments, cached)
		estimatedPending := enrichPayoutEstimates(respPayments)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"payments":          respPayments,
			"count":             len(cached),
			"cached_at":         lastTime.Format(time.RFC3339),
			"last_update":       lastUpd,
			"fresh":             isFresh,
			"error":             lastErr,
			"points":            pts,
			"estimated_pending": estimatedPending,
		})
	}
}

func handleRefresh(token string, db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}

		stats, err := fetchStats(token)
		if err != nil {
			jsonError(w, err.Error())
			return
		}

		now := time.Now().UTC()
		_, min, sec := now.Clock()
		if min%15 == 0 && sec < 5 {
			windowStart := now.Truncate(1 * time.Hour).Add(time.Duration(min) * time.Minute)
			var existingID int64
			db.QueryRow("SELECT id FROM wallet_stats WHERE created_at >= ? AND created_at < ? ORDER BY created_at ASC LIMIT 1",
				windowStart.Format(time.RFC3339),
				windowStart.Add(time.Minute).Format(time.RFC3339),
			).Scan(&existingID)

			if existingID == 0 {
				_, err = db.Exec(
					"INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?, ?, ?, ?)",
					int64(stats.PaidBytes), int64(stats.UnpaidBytes), windowStart.Format(time.RFC3339), now.Format(time.RFC3339),
				)
				if err != nil {
					jsonError(w, err.Error())
					return
				}
				checkTrafficSpike(db, stats.UnpaidBytes, now)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":      true,
			"paid_bytes":   stats.PaidBytes,
			"unpaid_bytes": stats.UnpaidBytes,
		})
	}
}

func handleRefreshPayout(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}

		resp, err := fetchPayouts(token)
		if err != nil {
			payoutCacheMu.Lock()
			payoutLastError = err.Error()
			payoutCacheMu.Unlock()
			jsonError(w, err.Error())
			return
		}

		if resp.Error != nil {
			payoutCacheMu.Lock()
			payoutLastError = resp.Error.Message
			payoutCacheMu.Unlock()
			jsonError(w, resp.Error.Message)
			return
		}

		pointEntries, _ := fetchPoints(token)
		pointsByPayment := make(map[string]float64)
		reliabilityByPayment := make(map[string]float64)
		var totalPoints float64
		var totalReliability float64
		for _, pe := range pointEntries {
			pointsByPayment[pe.AccountPaymentID] += float64(pe.PointValue) / 1e6
			totalPoints += float64(pe.PointValue) / 1e6
			if pe.Event == "payout_reliability" {
				reliabilityByPayment[pe.AccountPaymentID] += float64(pe.PointValue) / 1e6
				totalReliability += float64(pe.PointValue) / 1e6
			}
		}
		for i := range resp.AccountPayments {
			resp.AccountPayments[i].PointsEarned = pointsByPayment[resp.AccountPayments[i].PaymentID]
			resp.AccountPayments[i].ReliabilityPts = reliabilityByPayment[resp.AccountPayments[i].PaymentID]
		}

		payoutCacheMu.Lock()
		payoutCache = resp.AccountPayments
		payoutCacheTime = time.Now()
		payoutLastError = ""
		payoutLastUpdate = time.Now().UTC().Format(time.RFC3339)
		payoutLastPoints = totalPoints
		payoutCacheMu.Unlock()

		syncPayoutNotifyStore(resp.AccountPayments, true)

		respPayments := make([]payoutRecord, len(resp.AccountPayments))
		copy(respPayments, resp.AccountPayments)
		estimatedPending := enrichPayoutEstimates(respPayments)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":           true,
			"count":             len(resp.AccountPayments),
			"payments":          respPayments,
			"points":            totalPoints,
			"estimated_pending": estimatedPending,
		})
	}
}

// notifyStoreFile returns the persisted notification-dedup store path
// (env-overridable for tests and unusual installs).
func notifyStoreFile() string {
	if notifyStorePath != "" {
		return notifyStorePath
	}
	if p := os.Getenv("PAYOUT_NOTIFY_STORE"); p != "" {
		notifyStorePath = p
		return p
	}
	home, _ := os.UserHomeDir()
	notifyStorePath = filepath.Join(home, ".urnetwork", "payout_notified.json")
	return notifyStorePath
}

func saveNotifyStore() error {
	b, err := json.Marshal(notifyStore)
	if err != nil {
		return err
	}
	path := notifyStoreFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// syncPayoutNotifyStore diffs the fresh /account/payments list against the
// persisted notification-dedup store and announces only genuine changes.
//
// The store (keyed by payment_id) is the notification source of truth, not
// the in-memory payoutCache, so a process restart cannot re-announce every
// historical payout as new. On a cold start (no store file yet) the first
// successful fetch seeds a baseline WITHOUT sending anything; notifications
// fire only for changes after that baseline.
//
// notify=false callers (the lazy page-load cache path) only ensure the
// baseline exists and never announce; notify=true callers (explicit refresh)
// run the full diff.
//
// Concurrency: notifyStoreMu serializes all store access. Two racing refreshes
// apply in whichever order they acquire the lock, so an older fetch could
// theoretically overwrite a newer record; accepted for a single-operator
// dashboard (the lazy path never announces).
func syncPayoutNotifyStore(fresh []payoutRecord, notify bool) {
	notifyStoreMu.Lock()
	defer notifyStoreMu.Unlock()

	if notifyStore == nil {
		b, err := os.ReadFile(notifyStoreFile())
		switch {
		case err == nil:
			store := map[string]notifyRecord{}
			if uerr := json.Unmarshal(b, &store); uerr != nil {
				fmt.Printf("[webhook] notify store parse error: %v (will re-seed)\n", uerr)
				notifyStore = map[string]notifyRecord{}
				notifyStoreSeeded = false
			} else if len(store) == 0 {
				// Valid but empty: treat as unseeded so a truncated store
				// cannot cause every payout to be re-announced as new.
				notifyStore = store
				notifyStoreSeeded = false
			} else {
				notifyStore = store
				notifyStoreSeeded = true
			}
		case os.IsNotExist(err):
			notifyStore = map[string]notifyRecord{}
			notifyStoreSeeded = false
		default:
			fmt.Printf("[webhook] notify store read error: %v (will re-seed)\n", err)
			notifyStore = map[string]notifyRecord{}
			notifyStoreSeeded = false
		}
	}

	if !notifyStoreSeeded {
		// Cold start: baseline every known payout, announce nothing. An
		// empty/short first response (transient upstream glitch) must NOT
		// become the permanent baseline — that would re-announce every
		// payout as new on the next good fetch. Stay unseeded and retry.
		seeded := 0
		for _, p := range fresh {
			if p.TxHash == "" {
				continue
			}
			notifyStore[p.PaymentID] = notifyRecord{TxHash: p.TxHash, Completed: p.Completed}
			seeded++
		}
		if seeded == 0 {
			fmt.Println("[webhook] notify store: cold-start fetch had no payouts, not seeding yet")
			return
		}
		if err := saveNotifyStore(); err != nil {
			fmt.Printf("[webhook] notify store save error: %v (will retry seed)\n", err)
			return
		}
		notifyStoreSeeded = true
		return
	}

	if !notify {
		return
	}

	changed := false
	for _, p := range fresh {
		if p.TxHash == "" {
			continue
		}

		rec, ok := notifyStore[p.PaymentID]
		if !ok || rec.TxHash != p.TxHash {
			amount := fmt.Sprintf("$%.2f", p.TokenAmount)
			bytes := fmt.Sprintf("%.1f GB", float64(p.PayoutByteCount)/1e9)
			status := "⏳ Pending"
			if p.Completed {
				status = "✅ Completed"
			}
			notifySend(fmt.Sprintf("💰 **New Payout** %s\nAmount: %s · Data: %s\nChain: %s",
				status, amount, bytes, p.Blockchain))
			notifyStore[p.PaymentID] = notifyRecord{TxHash: p.TxHash, Completed: p.Completed}
			changed = true
			continue
		}

		if !rec.Completed && p.Completed {
			amount := fmt.Sprintf("$%.2f", p.TokenAmount)
			tx := p.TxHash
			if len(tx) > 12 {
				tx = tx[:12]
			}
			notifySend(fmt.Sprintf("✅ **Payout Completed**\nAmount: %s\nTx: %s",
				amount, tx+"…"))
			rec.Completed = true
			notifyStore[p.PaymentID] = rec
			changed = true
		}
		// Completed is terminal: if a later fetch reports the payout back as
		// pending, fall through with no state change (no flip back, so the
		// completion notification can never re-fire).
	}
	if changed {
		if err := saveNotifyStore(); err != nil {
			fmt.Printf("[webhook] notify store save error: %v\n", err)
		}
	}
}

func jsonError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(500)
	json.NewEncoder(w).Encode(map[string]string{
		"error": msg,
	})
}

// statsInterval returns the polling cadence from STATS_INTERVAL (default 15m,
// minimum 1m), matching the ALWAYS-used start of the poller loop.
func statsInterval() time.Duration {
	const def = 15 * time.Minute
	if s := os.Getenv("STATS_INTERVAL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d >= time.Minute {
			return d
		}
	}
	return def
}

func checkTrafficSpike(db *sql.DB, unpaidBytes uint64, now time.Time) {
	var prevUnpaid sql.NullInt64
	var prevAt sql.NullString
	db.QueryRow(
		"SELECT unpaid_bytes, created_at FROM wallet_stats ORDER BY id DESC LIMIT 1 OFFSET 1",
	).Scan(&prevUnpaid, &prevAt)
	if !prevUnpaid.Valid {
		return
	}
	// If the previous row is not a recent poll window, this is a backfill
	// after the poller dropped windows, not a genuine short-window burst.
	// Example above threshold with no gap guard: polls lost for
	// 04:30-05:45 lumped into one row, so the 06:00 delta read as an 18 GB
	// "15m spike" and alerted. Skip and log instead. The threshold is two
	// poll intervals so it stays correct if STATS_INTERVAL is raised.
	if prevAt.Valid {
		pt, perr := time.Parse(time.RFC3339, prevAt.String)
		if perr != nil {
			fmt.Printf("[webhook] traffic spike: could not parse prev created_at %q: %v (proceeding unguarded)\n",
				prevAt.String, perr)
		} else if now.Sub(pt) > 2*statsInterval() {
			fmt.Printf("[webhook] traffic spike skipped: prev row %s is %.0fm old (backfill, not a %s window)\n",
				pt.Format("15:04"), now.Sub(pt).Minutes(), statsInterval())
			return
		}
	}
	deltaBytes := int64(unpaidBytes) - prevUnpaid.Int64
	if deltaBytes <= 1_000_000_000 {
		return
	}
	deltaGB := float64(deltaBytes) / 1e9
	totalGB := float64(unpaidBytes) / 1e9
	nowStr := now.Format("15:04:05")
	sendDiscordNotification(fmt.Sprintf(
		"🛫 **Traffic Spike**\n"+
			"```\n"+
			"┌──────────────────────┬────────────┐\n"+
			"├──────────────────────┼────────────┤\n"+
			"│ 15m Delta            │ %10s │\n"+
			"│ Total Unpaid         │ %10s │\n"+
			"│ At (UTC)             │ %10s │\n"+
			"└──────────────────────┴────────────┘\n"+
			"```",
		fmt.Sprintf("+%.2f GB", deltaGB),
		fmt.Sprintf("%.2f GB", totalGB),
		nowStr,
	))
}

func discordWebhookURL() string {
	if url := os.Getenv("DISCORD_WEBHOOK_URL"); url != "" {
		return url
	}
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".urnetwork", "discord_webhook"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func sendDiscordNotification(content string) {
	url := discordWebhookURL()
	if url == "" {
		fmt.Println("[webhook] DISCORD_WEBHOOK_URL not set and no ~/.urnetwork/discord_webhook file")
		return
	}
	go func() {
		body, _ := json.Marshal(map[string]string{"content": content})
		resp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
		if err != nil {
			fmt.Printf("[webhook] POST error: %v\n", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode > 299 {
			b, _ := io.ReadAll(resp.Body)
			fmt.Printf("[webhook] %d: %s\n", resp.StatusCode, string(b))
		}
	}()
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"version": Version,
		"uptime":  time.Since(startTime).String(),
	})
}

func handleNetworkName(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"network_name": networkNameFromJWT(token),
		})
	}
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}
