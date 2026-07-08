package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
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
	PaymentID      string  `json:"payment_id"`
	TokenAmount    float64 `json:"token_amount"`
	PayoutByteCount int64  `json:"payout_byte_count"`
	PointsEarned   float64 `json:"points_earned"`
	ReliabilityPts float64 `json:"reliability_points"`
	Completed      bool    `json:"completed"`
	Canceled       bool    `json:"canceled"`
	CreateTime     string  `json:"create_time"`
	CompleteTime   string  `json:"complete_time"`
	PaymentTime    string  `json:"payment_time"`
	TxHash         string  `json:"tx_hash"`
	WalletAddress  string  `json:"wallet_address"`
	Blockchain     string  `json:"blockchain"`
	TokenType      string  `json:"token_type"`
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
	Version    = "dev"
	startTime  = time.Now()

	payoutCache      []payoutRecord
	payoutCacheMu    sync.RWMutex
	payoutCacheTime  time.Time
	payoutLastError  string
	payoutLastUpdate string
	payoutLastPoints float64

	httpClient = &http.Client{Timeout: 15 * time.Second}
	cachedJWT  string
)

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
	default:
		fmt.Println(`Usage:
  stats_tracker run                    — start polling daemon
  stats_tracker serve [port]          — start HTTP server (default :3001)
  stats_tracker import <file.json>     — import bayouash export
  stats_tracker history                — print stored history`)
	}
}

func dbPath() string {
	if p := os.Getenv("STATS_DB"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".urnetwork", "wallet_stats.db")
}

func jwtPath() string {
	if p := os.Getenv("JWT_PATH"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".urnetwork", "jwt")
}

func openDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath())
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

	interval := 15 * time.Minute
	if s := os.Getenv("STATS_INTERVAL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d >= time.Minute {
			interval = d
		}
	}

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
	mux.HandleFunc("/api/clear", handleClear(db))
	mux.HandleFunc("/api/refresh-payout", handleRefreshPayout(token))
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/", handleIndex)

	fmt.Printf("[serve] listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
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
				payoutLastPoints = totalPoints
				payoutCacheMu.Unlock()
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

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"payments":    cached,
			"count":       len(cached),
			"cached_at":   lastTime.Format(time.RFC3339),
			"last_update": lastUpd,
			"fresh":       isFresh,
			"error":       lastErr,
			"points":      pts,
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

func handleClear(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}

		if _, err := db.Exec("DELETE FROM wallet_stats"); err != nil {
			jsonError(w, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
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
		oldCache := payoutCache
		payoutCache = resp.AccountPayments
		payoutCacheTime = time.Now()
		payoutLastError = ""
		payoutLastUpdate = time.Now().UTC().Format(time.RFC3339)
		payoutLastPoints = totalPoints
		payoutCacheMu.Unlock()

		notifyPayoutChanges(oldCache, resp.AccountPayments)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   true,
			"count":     len(resp.AccountPayments),
			"payments":  resp.AccountPayments,
			"points":    totalPoints,
		})
	}
}

func notifyPayoutChanges(old []payoutRecord, new []payoutRecord) {
	oldByTx := make(map[string]payoutRecord)
	for _, p := range old {
		if p.TxHash != "" {
			oldByTx[p.TxHash] = p
		}
	}

	for _, p := range new {
		if p.TxHash == "" {
			continue
		}

		oldP, existed := oldByTx[p.TxHash]

		if !existed {
			amount := fmt.Sprintf("$%.2f", p.TokenAmount)
			bytes := fmt.Sprintf("%.1f GB", float64(p.PayoutByteCount)/1e9)
			status := "⏳ Pending"
			if p.Completed {
				status = "✅ Completed"
			}
			sendDiscordNotification(fmt.Sprintf("💰 **New Payout** %s\nAmount: %s · Data: %s\nChain: %s",
				status, amount, bytes, p.Blockchain))
			continue
		}

		if !oldP.Completed && p.Completed {
			amount := fmt.Sprintf("$%.2f", p.TokenAmount)
			sendDiscordNotification(fmt.Sprintf("✅ **Payout Completed**\nAmount: %s\nTx: %s",
				amount, p.TxHash[:12]+"…"))
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

func discordWebhookURL() string {
	return os.Getenv("DISCORD_WEBHOOK_URL")
}

func sendDiscordNotification(content string) {
	url := discordWebhookURL()
	if url == "" {
		return
	}
	go func() {
		body, _ := json.Marshal(map[string]string{"content": content})
		resp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
		if err == nil {
			resp.Body.Close()
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

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}
