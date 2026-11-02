package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bittensor SN25 epoch earnings.
//
// The coordinator exposes two read-only views that matter here:
//
//	GET /account/epochs?limit=N  -> {"epochs":[{epoch,start_millis,end_millis,points,share_bps}]}
//	GET /sn/epoch               -> {epoch,start_block,commit_deadline_block,finalize_block,
//	                                t_epoch_blocks,chain_id,netuid,...}
//
// Epochs are keyed by their number and never by time, so a re-poll overwrites a
// row rather than appending one: the partial points of an open epoch have to be
// able to move. That is the opposite of wallet_stats, which is append-only.
//
// Nothing here touches wallet_stats, so the 15-minute grid, its cleanup and the
// cross-box merge keep working exactly as they do today.

// commitWindowBlocks is the gap between the end of the earning window and the
// commit deadline. The coordinator reports the deadline but not the earning
// boundary, so the boundary is derived from t_epoch_blocks.
const commitWindowBlocks = 1200

// raoPerAlpha is the fixed-point scale the coordinator reports alpha in.
const raoPerAlpha = 1e9

// epochRow is one finalized epoch as the dashboard cares about it.
type epochRow struct {
	Epoch       int64   `json:"epoch"`
	Points      float64 `json:"points"`
	ShareBps    int64   `json:"share_bps"`
	AlphaRao    *int64  `json:"-"`          // as the coordinator reports it
	Settlement  string  `json:"settlement"` // "v1" or "v2"
	StartMillis int64   `json:"start_millis"`
	EndMillis   int64   `json:"end_millis"`
	Finalized   int64   `json:"finalized"` // 1 once the epoch has closed
}

// epochView is what the page receives: the same row with alpha in whole
// tokens. Storage keeps rao, so a row can never be written back at the wrong
// scale by accident.
type epochView struct {
	Epoch       int64    `json:"epoch"`
	Points      float64  `json:"points"`
	ShareBps    int64    `json:"share_bps"`
	Alpha       *float64 `json:"alpha"` // nil until the epoch's root lands
	Settlement  string   `json:"settlement"`
	StartMillis int64    `json:"start_millis"`
	EndMillis   int64    `json:"end_millis"`
	Finalized   int64    `json:"finalized"`
}

// snEpoch is the live epoch's block geometry.
type snEpoch struct {
	Epoch       int64  `json:"epoch"`
	StartBlock  int64  `json:"start_block"`
	CommitBlock int64  `json:"commit_deadline_block"`
	Finalize    int64  `json:"finalize_block"`
	TEpochBlock int64  `json:"t_epoch_blocks"`
	EarningEnd  int64  `json:"earning_end_block"` // stored, derived on insert
	ChainID     int64  `json:"chain_id"`
	Netuid      int64  `json:"netuid"`
	NoID        string `json:"no_id"`
}

// earningEndBlock is where points stop accruing.
func (e snEpoch) earningEndBlock() int64 {
	if e.TEpochBlock > 0 {
		return e.StartBlock + e.TEpochBlock
	}
	return e.CommitBlock - commitWindowBlocks
}

// epochAPIAccount is the wire shape from /account/epochs.
type epochAPIAccount struct {
	Epochs []struct {
		Epoch        int64   `json:"epoch"`
		StartMillis  int64   `json:"start_millis"`
		EndMillis    int64   `json:"end_millis"`
		Points       float64 `json:"points"`
		ShareBps     int64   `json:"share_bps"`
		SettlementV  string  `json:"settlement_version"`
		AlphaRao     *int64  `json:"alpha_rao"`
		BlockchainID string  `json:"blockchain_id"`
	} `json:"epochs"`
}

func ensureEpochSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS epoch_stats (
			epoch INTEGER PRIMARY KEY,
			points REAL NOT NULL DEFAULT 0,
			share_bps INTEGER NOT NULL DEFAULT 0,
			alpha_rao INTEGER,
			settlement TEXT NOT NULL DEFAULT '',
			start_millis INTEGER NOT NULL DEFAULT 0,
			end_millis INTEGER NOT NULL DEFAULT 0,
			finalized INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS epoch_poll_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			last_attempt TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS sn_epoch_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			epoch INTEGER NOT NULL DEFAULT 0,
			start_block INTEGER NOT NULL DEFAULT 0,
			earning_end_block INTEGER NOT NULL DEFAULT 0,
			commit_block INTEGER NOT NULL DEFAULT 0,
			finalize_block INTEGER NOT NULL DEFAULT 0,
			t_epoch_blocks INTEGER NOT NULL DEFAULT 0,
			chain_id INTEGER NOT NULL DEFAULT 0,
			netuid INTEGER NOT NULL DEFAULT 0,
			fetched_at TEXT NOT NULL,
			last_attempt TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("epoch schema: %w", err)
		}
	}
	/* add the columns a pre-existing database will not have */
	for _, c := range []struct{ table, col, decl string }{
		{"epoch_stats", "finalized", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := addColumnIfMissing(db, c.table, c.col, c.decl); err != nil {
			return err
		}
	}
	/* epochs stored before the marker existed are closed by the clock */
	if _, err := db.Exec(`UPDATE epoch_stats SET finalized = 1
		WHERE finalized = 0 AND end_millis > 0 AND end_millis <= ?`,
		time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("epoch schema: backfill finalized: %w", err)
	}
	return nil
}

func addColumnIfMissing(db *sql.DB, table, col, decl string) error {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n)
	if err != nil {
		return fmt.Errorf("epoch schema: inspect %s.%s: %w", table, col, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + decl); err != nil {
		return fmt.Errorf("epoch schema: add %s.%s: %w", table, col, err)
	}
	return nil
}

// storeEpochs upserts by epoch number. An epoch that is still open reports
// partial points, so the update is unconditional rather than "only if greater".
func storeEpochs(db *sql.DB, rows []epochRow, now time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO epoch_stats
		(epoch, points, share_bps, alpha_rao, settlement, start_millis, end_millis, finalized, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(epoch) DO UPDATE SET
			/* once an epoch has closed its points are final: a later poll (or a
			   partial response) must not overwrite them */
			points=CASE WHEN epoch_stats.finalized=1 THEN epoch_stats.points ELSE excluded.points END,
			share_bps=CASE WHEN epoch_stats.finalized=1 THEN epoch_stats.share_bps ELSE excluded.share_bps END,
			alpha_rao=COALESCE(excluded.alpha_rao, epoch_stats.alpha_rao),
			settlement=excluded.settlement,
			start_millis=excluded.start_millis,
			end_millis=excluded.end_millis,
			finalized=MAX(epoch_stats.finalized, excluded.finalized),
			updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	stamp := now.UTC().Format(time.RFC3339)
	for _, r := range rows {
		var alpha interface{}
		if r.AlphaRao != nil {
			alpha = *r.AlphaRao
		}
		finalized := 0
		if r.EndMillis > 0 && r.EndMillis <= now.UnixMilli() {
			finalized = 1
		}
		if _, err := stmt.Exec(r.Epoch, r.Points, r.ShareBps, alpha, r.Settlement,
			r.StartMillis, r.EndMillis, finalized, stamp); err != nil {
			return fmt.Errorf("store epoch %d: %w", r.Epoch, err)
		}
	}
	return tx.Commit()
}

func storeSNEpoch(db *sql.DB, e snEpoch, now time.Time) error {
	_, err := db.Exec(`INSERT INTO sn_epoch_state
		(id, epoch, start_block, earning_end_block, commit_block, finalize_block,
		 t_epoch_blocks, chain_id, netuid, fetched_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			epoch=excluded.epoch, start_block=excluded.start_block,
			earning_end_block=excluded.earning_end_block, commit_block=excluded.commit_block,
			finalize_block=excluded.finalize_block, t_epoch_blocks=excluded.t_epoch_blocks,
			chain_id=excluded.chain_id, netuid=excluded.netuid, fetched_at=excluded.fetched_at`,
		e.Epoch, e.StartBlock, e.earningEndBlock(), e.CommitBlock, e.Finalize,
		e.TEpochBlock, e.ChainID, e.Netuid, now.UTC().Format(time.RFC3339))
	return err
}

// storeEpochAttempt records when the poll last ran and what it said, so a
// dashboard with no epochs can explain itself instead of showing "never polled".
func storeEpochAttempt(db *sql.DB, at time.Time, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	/* its own table: an attempt is not a fetched epoch, and writing one into
	   sn_epoch_state would serve a zero epoch to the page */
	db.Exec(`INSERT INTO epoch_poll_state (id, last_attempt, last_error)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			last_attempt=excluded.last_attempt, last_error=excluded.last_error`,
		at.UTC().Format(time.RFC3339), msg)
}

func readEpochAttempt(db *sql.DB) (time.Time, string) {
	var at, msg string
	if err := db.QueryRow(`SELECT last_attempt, last_error FROM epoch_poll_state WHERE id = 1`).Scan(&at, &msg); err != nil {
		return time.Time{}, ""
	}
	ts, _ := time.Parse(time.RFC3339, at)
	return ts, msg
}

func readSNEpoch(db *sql.DB) (snEpoch, time.Time, error) {
	var e snEpoch
	var fetched string
	err := db.QueryRow(`SELECT epoch, start_block, earning_end_block, commit_block,
		finalize_block, t_epoch_blocks, chain_id, netuid, fetched_at
		FROM sn_epoch_state WHERE id = 1`).
		Scan(&e.Epoch, &e.StartBlock, &e.EarningEnd, &e.CommitBlock, &e.Finalize,
			&e.TEpochBlock, &e.ChainID, &e.Netuid, &fetched)
	if err != nil {
		return e, time.Time{}, err
	}
	ts, _ := time.Parse(time.RFC3339, fetched)
	return e, ts, nil
}

func readEpochs(db *sql.DB, limit int) ([]epochView, error) {
	if limit <= 0 {
		limit = 104
	}
	rows, err := db.Query(`SELECT epoch, points, share_bps, alpha_rao, settlement,
		start_millis, end_millis, finalized FROM epoch_stats ORDER BY epoch DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]epochView, 0, limit)
	for rows.Next() {
		var r epochView
		var alpha sql.NullFloat64
		if err := rows.Scan(&r.Epoch, &r.Points, &r.ShareBps, &alpha, &r.Settlement,
			&r.StartMillis, &r.EndMillis, &r.Finalized); err != nil {
			return nil, err
		}
		if alpha.Valid {
			/* stored as rao; everything above this layer speaks alpha */
			v := alpha.Float64 / raoPerAlpha
			r.Alpha = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// settlementFor reports which payout model an epoch settled under. Epochs are
// keyed by number, and the v2 switch happened at a single epoch for the whole
// subnet, so the caller passes the first v2 epoch once it is known.
func settlementFor(epoch, firstV2 int64, fromCoordinator string) string {
	/* the coordinator is the authority when it says something, but only a
	   value this layer recognises: it reaches the page's HTML */
	if v := strings.ToLower(strings.TrimSpace(fromCoordinator)); v == "v1" || v == "v2" {
		return v
	}
	if firstV2 > 0 && epoch >= firstV2 {
		return "v2"
	}
	return "v1"
}

// fetchJSON is the one place the bearer token is attached.
func fetchJSON(rawURL, token string, out interface{}) error {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func fetchEpochs(token string, firstV2 int64) ([]epochRow, error) {
	var acc epochAPIAccount
	if err := fetchJSON("https://api.bringyour.com/account/epochs?limit=104", token, &acc); err != nil {
		return nil, err
	}
	out := make([]epochRow, 0, len(acc.Epochs))
	for _, e := range acc.Epochs {
		r := epochRow{
			Epoch:       e.Epoch,
			Points:      e.Points,
			ShareBps:    e.ShareBps,
			Settlement:  settlementFor(e.Epoch, firstV2, e.SettlementV),
			StartMillis: e.StartMillis,
			EndMillis:   e.EndMillis,
		}
		if e.AlphaRao != nil {
			v := *e.AlphaRao
			r.AlphaRao = &v
		}
		out = append(out, r)
	}
	return out, nil
}

func fetchSNEpoch(token string) (snEpoch, error) {
	var e snEpoch
	if err := fetchJSON("https://api.bringyour.com/sn/epoch", token, &e); err != nil {
		return e, err
	}
	/* decoding {} succeeds, and a zero epoch would then overwrite real geometry
	   with a negative earning window. Reject anything that cannot be a real
	   epoch rather than trusting the shape. */
	if e.Epoch <= 0 || e.StartBlock <= 0 || e.CommitBlock <= e.StartBlock || e.Finalize <= e.CommitBlock {
		return snEpoch{}, fmt.Errorf("sn/epoch: implausible geometry (epoch %d, start %d, commit %d, finalize %d)",
			e.Epoch, e.StartBlock, e.CommitBlock, e.Finalize)
	}
	/* the earning boundary is derived, so it gets its own check: a bad
	   t_epoch_blocks would otherwise place it outside the epoch */
	if end := e.earningEndBlock(); end <= e.StartBlock || end > e.CommitBlock {
		return snEpoch{}, fmt.Errorf("sn/epoch: derived earning end %d outside (%d, %d]",
			end, e.StartBlock, e.CommitBlock)
	}
	return e, nil
}

// epochsPayload is the one document both endpoints serve, so a client cannot
// see different shapes from the read and the refresh.
func epochsPayload(db *sql.DB) (map[string]interface{}, error) {
	epochs, err := readEpochs(db, 104)
	if err != nil {
		return nil, err
	}
	payload := map[string]interface{}{"epochs": epochs}
	if at, msg := readEpochAttempt(db); !at.IsZero() {
		payload["last_attempt"] = at.UTC().Format(time.RFC3339)
		payload["last_error"] = msg
	}
	state, fetchedAt, stateErr := readSNEpoch(db)
	switch {
	case stateErr == nil:
		payload["sn_epoch"] = state
		payload["sn_epoch_fetched_at"] = fetchedAt.Format(time.RFC3339)
	case errors.Is(stateErr, sql.ErrNoRows):
		/* a fresh install has no epoch fetched yet; that is not an error */
	default:
		/* anything else is a real read failure and must not be hidden */
		return nil, stateErr
	}
	return payload, nil
}

// handleEpochs serves the Bittensor page: the live epoch geometry plus the
// finalized history, in one round trip.
func handleEpochs(db *sql.DB, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		payload, err := epochsPayload(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payload)
	}
}

// pollEpochs is the hourly refresh. Epochs move on a seven-day clock, so
// polling them at the 15-minute stats cadence would be waste; the dashboard
// also refreshes on demand through /api/refresh-epochs.
// epochPollMu serialises whole poll operations: a manual refresh racing the
// hourly tick would otherwise interleave two fetches and two stores, and the
// slower writer could land an older epoch row last.
var epochPollMu sync.Mutex

func pollEpochs(db *sql.DB, token string, firstV2 int64) (err error) {
	epochPollMu.Lock()
	defer epochPollMu.Unlock()
	defer func() { storeEpochAttempt(db, time.Now(), err) }()
	/* fetch both before storing either: committing history and then failing on
	   the state would leave a page showing epoch N+1 next to epoch N's geometry */
	rows, err := fetchEpochs(token, firstV2)
	if err != nil {
		return err
	}
	state, err := fetchSNEpoch(token)
	if err != nil {
		return err
	}
	if err := storeEpochs(db, rows, time.Now()); err != nil {
		return err
	}
	return storeSNEpoch(db, state, time.Now())
}

// firstV2Epoch is the first epoch that settled under the v2 payout model. The
// v2 switch happened once for the whole subnet, so it is a boundary rather than
// a per-epoch flag; it is a env override because it must be set per deployment.
func firstV2Epoch() int64 {
	if v := os.Getenv("EPOCH_FIRST_V2"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 4
}

// handleRefreshEpochs forces a fetch, for the page's Refresh button.
func handleRefreshEpochs(db *sql.DB, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
			return
		}
		/* a client that hung up while the poll was queued behind the hourly
		   tick must not hold the lock through a fetch nobody will read */
		if err := r.Context().Err(); err != nil {
			w.WriteHeader(http.StatusRequestTimeout)
			json.NewEncoder(w).Encode(map[string]string{"error": "client went away"})
			return
		}
		if err := pollEpochs(db, token, firstV2Epoch()); err != nil {
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		payload, err := epochsPayload(db)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		payload["ok"] = true
		json.NewEncoder(w).Encode(payload)
	}
}
