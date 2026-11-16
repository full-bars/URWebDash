package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const gb = int64(1_000_000_000)

func servedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("STATS_DB", filepath.Join(t.TempDir(), "served.db"))
	db, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func blockAt(block int64, bytes int64, asOf time.Time) servedBlockWire {
	return servedBlockWire{
		BlockNumber: block, StartMillis: 1_000, EndMillis: 2_000,
		ServedByteCount: bytes, AsOfMillis: asOf.UnixMilli(), Epoch: 7,
	}
}

var servedT0 = time.Date(2030, 11, 10, 10, 0, 0, 0, time.UTC)

func TestServedSampleStoresOnceAndReportsFreshness(t *testing.T) {
	db := servedTestDB(t)
	fresh, err := storeServedSample(db, blockAt(1, 10*gb, servedT0), nil, servedT0)
	if err != nil || !fresh {
		t.Fatalf("first sample = (%v, %v), want fresh", fresh, err)
	}
	fresh, err = storeServedSample(db, blockAt(1, 10*gb, servedT0), nil, servedT0.Add(time.Hour))
	if err != nil || fresh {
		t.Fatalf("same as-of time = (%v, %v), want not fresh", fresh, err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM block_samples`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestServedSampleRejectsImplausibleReadings(t *testing.T) {
	db := servedTestDB(t)
	for name, blk := range map[string]servedBlockWire{
		"no as-of":       blockAt(1, gb, time.Time{}),
		"no block":       blockAt(0, gb, servedT0),
		"negative bytes": blockAt(1, -1, servedT0),
	} {
		if fresh, err := storeServedSample(db, blk, nil, servedT0); err == nil || fresh {
			t.Errorf("%s: stored (%v, %v), want a refusal", name, fresh, err)
		}
	}
}

func TestServedIntervals(t *testing.T) {
	mk := func(block, bytes int64, h int) servedSample {
		return servedSample{AsOfMillis: servedT0.Add(time.Duration(h) * time.Hour).UnixMilli(), BlockNumber: block, BlockBytes: bytes}
	}
	got := servedIntervals([]servedSample{
		mk(1, 10*gb, 0), mk(1, 16*gb, 1), mk(1, 15*gb, 2), mk(2, 1*gb, 3), mk(2, 4*gb, 5),
	})
	if len(got) != 4 {
		t.Fatalf("intervals = %d, want 4", len(got))
	}
	if got[0].Kind != servedKindInterval || got[0].Bytes == nil || *got[0].Bytes != 6*gb {
		t.Errorf("interval 0 = %+v, want 6 GB", got[0])
	}
	if want := float64(6*gb) / 3600; got[0].BytesPerSec != want {
		t.Errorf("rate = %v, want %v", got[0].BytesPerSec, want)
	}
	if got[1].Kind != servedKindCorrection || got[1].Bytes != nil {
		t.Errorf("a figure that went backwards = %+v, want a correction with no bytes", got[1])
	}
	if got[2].Kind != servedKindRollover || got[2].Bytes != nil {
		t.Errorf("a new block = %+v, want a rollover with no bytes", got[2])
	}
	if got[3].Kind != servedKindInterval || *got[3].Bytes != 3*gb || got[3].ToMillis-got[3].FromMillis != int64(2*time.Hour/time.Millisecond) {
		t.Errorf("interval 3 = %+v, want 3 GB over 2h", got[3])
	}
	if len(servedIntervals(nil)) != 0 || len(servedIntervals([]servedSample{mk(1, 1, 0)})) != 0 {
		t.Error("fewer than two samples must give no intervals")
	}
}

// The response shape the server sends, as documented in its source.
const servedResponse = `{
 "epochs":[
  {"epoch":6,"start_millis":1,"end_millis":2,"points":1.5,"share_bps":10,"served_byte_count":7000000000},
  {"epoch":5,"start_millis":0,"end_millis":1,"points":0,"share_bps":0}],
 "current_epoch":{"epoch":7,"start_millis":2,"end_millis":9,"served_byte_count":3000000000,"as_of_millis":%d},
 "current_block":{"block_number":42,"start_millis":2,"end_millis":9,"served_byte_count":%d,"as_of_millis":%d,"epoch":7}
}`

func servedServer(t *testing.T, body func() string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/account/epochs" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body()))
	}))
	t.Cleanup(srv.Close)
	old := coordBaseURL
	coordBaseURL = srv.URL
	t.Cleanup(func() { coordBaseURL = old })
}

func TestPollServedParsesTheServerResponseAndKeepsEpochFigures(t *testing.T) {
	db := servedTestDB(t)
	asOf := servedT0.UnixMilli()
	servedServer(t, func() string {
		return servedResponseWith(asOf, 5*gb, asOf)
	})
	// the epoch rows exist first, as pollEpochs stores them before the figures
	if err := storeEpochs(db, []epochRow{{Epoch: 6, Points: 1.5, StartMillis: 1, EndMillis: 2}}, servedT0); err != nil {
		t.Fatal(err)
	}
	if err := pollServed(db, "tok"); err != nil {
		t.Fatalf("pollServed: %v", err)
	}
	p, err := buildServedPayload(db)
	if err != nil || p.Block == nil {
		t.Fatalf("payload = %+v, %v", p, err)
	}
	if p.Block.BlockNumber != 42 || p.Block.BlockBytes != 5*gb || p.Block.AsOfMillis != asOf {
		t.Errorf("block = %+v", p.Block)
	}
	if p.Block.Epoch == nil || *p.Block.Epoch != 7 || p.Block.EpochBytes == nil || *p.Block.EpochBytes != 3*gb {
		t.Errorf("open epoch = %+v", p.Block)
	}
	epochs, err := readEpochs(db, 10)
	if err != nil || len(epochs) != 1 || epochs[0].ServedBytes == nil || *epochs[0].ServedBytes != 7*gb {
		t.Fatalf("finalized epoch served bytes = %+v, %v", epochs, err)
	}
}

func servedResponseWith(epochAsOf, blockBytes, blockAsOf int64) string {
	return fmt.Sprintf(servedResponse, epochAsOf, blockBytes, blockAsOf)
}

func TestPollServedTwiceAtOneAsOfTimeStoresOneSample(t *testing.T) {
	db := servedTestDB(t)
	asOf := servedT0.UnixMilli()
	servedServer(t, func() string { return servedResponseWith(asOf, 5*gb, asOf) })
	for i := 0; i < 2; i++ {
		if err := pollServed(db, "tok"); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM block_samples`).Scan(&n)
	if n != 1 {
		t.Fatalf("samples = %d, want 1", n)
	}
}

func TestPollServedWithoutTheFieldsExplainsItself(t *testing.T) {
	db := servedTestDB(t)
	servedServer(t, func() string {
		return `{"epochs":[{"epoch":5,"start_millis":0,"end_millis":1,"points":0,"share_bps":0}]}`
	})
	err := pollServed(db, "tok")
	if err == nil || !strings.Contains(err.Error(), "no served-byte figures") {
		t.Fatalf("err = %v, want the no-figures message", err)
	}
}

func TestServedSpikeAlertFiresOnAScaledInterval(t *testing.T) {
	srv, closeSrv := newSpikeCountServer()
	defer closeSrv()
	t.Setenv("DISCORD_WEBHOOK_URL", srv.URL)
	t.Setenv("SPIKE_THRESHOLD", "1GB")
	db := servedTestDB(t)

	storeServedSample(db, blockAt(1, 10*gb, servedT0), nil, servedT0)
	// 8 GB in 1h is 2 GB per 15 minutes: over a 1 GB threshold
	fresh, _ := storeServedSample(db, blockAt(1, 18*gb, servedT0.Add(time.Hour)), nil, servedT0)
	if !fresh {
		t.Fatal("second sample should be fresh")
	}
	checkServedSpike(db, servedT0.Add(time.Hour).UnixMilli(), servedT0.Add(3*time.Hour))
	for i := 0; i < 50 && srv.count() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.count() != 1 {
		t.Fatalf("alerts = %d, want 1", srv.count())
	}
	body := srv.lastBody()
	for _, want := range []string{"8.00 GB", "18.00 GB", "Counted through", alertClock(servedT0.Add(time.Hour))} {
		if !strings.Contains(body, want) {
			t.Errorf("alert %q does not contain %q", body, want)
		}
	}
}

func TestServedSpikeAlertStaysQuietWhenItShould(t *testing.T) {
	srv, closeSrv := newSpikeCountServer()
	defer closeSrv()
	t.Setenv("DISCORD_WEBHOOK_URL", srv.URL)
	t.Setenv("SPIKE_THRESHOLD", "1GB")
	db := servedTestDB(t)

	check := func(name string, prev, cur servedBlockWire) {
		t.Helper()
		before := srv.count()
		storeServedSample(db, prev, nil, servedT0)
		storeServedSample(db, cur, nil, servedT0)
		checkServedSpike(db, cur.AsOfMillis, servedT0)
		time.Sleep(60 * time.Millisecond)
		if srv.count() != before {
			t.Errorf("%s: sent an alert", name)
		}
	}
	h := func(n int) time.Time { return servedT0.Add(time.Duration(n) * 24 * time.Hour) }
	check("small interval", blockAt(1, 10*gb, h(0)), blockAt(1, 10*gb+gb/10, h(0).Add(time.Hour)))
	check("block rollover", blockAt(2, 10*gb, h(1)), blockAt(3, 90*gb, h(1).Add(time.Hour)))
	check("revised downwards", blockAt(4, 50*gb, h(2)), blockAt(4, 10*gb, h(2).Add(time.Hour)))
	check("backfill lump", blockAt(5, 10*gb, h(3)), blockAt(5, 90*gb, h(3).Add(7*time.Hour)))
}

func TestServedAlertStylesAllProduceValidJSON(t *testing.T) {
	for _, style := range []string{"embed", "hero-card", "story", "plain-lines", "ascii-table", "quote", "bullets", "local-clock", "ansi"} {
		out := formatServedSpikeAlert(style, time.Hour, 8*gb, 18*gb, servedT0, servedT0)
		var v map[string]any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Errorf("%s: invalid JSON: %v", style, err)
		}
	}
}

func TestServedRouteAndDashboardCarryTheSeries(t *testing.T) {
	db := servedTestDB(t)
	storeServedSample(db, blockAt(1, 10*gb, servedT0), nil, servedT0)
	storeServedSample(db, blockAt(1, 12*gb, servedT0.Add(time.Hour)), nil, servedT0)

	rec := httptest.NewRecorder()
	handleServed(db)(rec, httptest.NewRequest("GET", "/api/served", nil))
	var p servedPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Samples != 2 || len(p.Intervals) != 1 || p.Intervals[0].Bytes == nil || *p.Intervals[0].Bytes != 2*gb {
		t.Fatalf("payload = %+v", p)
	}
	d, _, _ := buildDashData(db, "")
	if d.Served.Block == nil || d.Served.Block.BlockBytes != 12*gb {
		t.Fatalf("dashboard served = %+v", d.Served)
	}
}

func TestServedLast24hNeedsASampleFromADayAgoInTheSameBlock(t *testing.T) {
	db := servedTestDB(t)
	storeServedSample(db, blockAt(1, 10*gb, servedT0), nil, servedT0)
	storeServedSample(db, blockAt(1, 22*gb, servedT0.Add(12*time.Hour)), nil, servedT0)
	p, _ := buildServedPayload(db)
	if p.Last24h != nil {
		t.Fatalf("12h of history gave a 24h figure: %+v", p.Last24h)
	}
	storeServedSample(db, blockAt(1, 40*gb, servedT0.Add(25*time.Hour)), nil, servedT0)
	p, _ = buildServedPayload(db)
	if p.Last24h == nil || p.Last24h.Bytes != 30*gb || p.Last24h.FromMillis != servedT0.UnixMilli() {
		t.Fatalf("last24h = %+v, want 30 GB from the first sample", p.Last24h)
	}
	// a sample from a day ago in another block gives nothing
	storeServedSample(db, blockAt(2, 5*gb, servedT0.Add(49*time.Hour)), nil, servedT0)
	storeServedSample(db, blockAt(2, 9*gb, servedT0.Add(50*time.Hour)), nil, servedT0)
	p, _ = buildServedPayload(db)
	if p.Last24h != nil {
		t.Fatalf("across a block change = %+v, want none", p.Last24h)
	}
}

func postTimezone(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/timezone", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handleTimezone(rec, req)
	return rec
}

func TestTimezoneHandlerSavesAValidZoneAndRefusesTheRest(t *testing.T) {
	t.Setenv("URWEBDASH_HOME", t.TempDir())
	if rec := postTimezone(t, `{"timezone":"America/Los_Angeles"}`); rec.Code != 200 {
		t.Fatalf("valid zone = %d %s", rec.Code, rec.Body)
	}
	if got := displayLocation().String(); got != "America/Los_Angeles" {
		t.Fatalf("saved zone = %q", got)
	}
	for _, bad := range []string{`{"timezone":"Not/AZone"}`, `{"timezone":""}`, `{"timezone":"Local"}`, `not json`} {
		before := displayLocation().String()
		if rec := postTimezone(t, bad); rec.Code == 200 && strings.Contains(rec.Body.String(), `"timezone"`) {
			t.Errorf("%s was accepted: %s", bad, rec.Body)
		}
		if displayLocation().String() != before {
			t.Errorf("%s changed the saved zone", bad)
		}
	}
	// a request without a JSON content type is refused like the other POST routes
	req := httptest.NewRequest("POST", "/api/timezone", strings.NewReader(`{"timezone":"UTC"}`))
	rec := httptest.NewRecorder()
	handleTimezone(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("no content type = %d, want 403", rec.Code)
	}
}

func TestAlertsUseTheWeekWordingAndTheSavedZone(t *testing.T) {
	t.Setenv("URWEBDASH_HOME", t.TempDir())
	postTimezone(t, `{"timezone":"America/Los_Angeles"}`)
	at := time.Date(2030, 11, 10, 19, 51, 0, 0, time.UTC) // 11:51 in Los Angeles
	if got := alertClock(at); got != "11:51 PST" {
		t.Fatalf("alertClock = %q, want 11:51 PST", got)
	}
	for _, style := range []string{"embed", "hero-card", "story", "plain-lines", "ascii-table", "quote", "bullets", "local-clock", "ansi", "box-table"} {
		out := formatSpikeAlert(style, time.Hour, 8, 18, at)
		if strings.Contains(strings.ToLower(out), "unpaid") {
			t.Errorf("%s still says unpaid: %s", style, out)
		}
		if strings.Contains(out, " UTC") || strings.Contains(out, "(UTC)") {
			t.Errorf("%s still shows UTC: %s", style, out)
		}
		if style != "local-clock" && !strings.Contains(out, "11:51 PST") && !strings.Contains(out, "11:51") {
			t.Errorf("%s does not show the local time: %s", style, out)
		}
	}
}

func TestServedTotalJoinsTheOldCounterToTheServerWeeks(t *testing.T) {
	db := servedTestDB(t)
	day := func(d string) string { return d + "T00:00:00Z" }
	put := func(at string, paid, unpaid int64) {
		db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?,?,?,?)", paid, unpaid, at, at)
	}
	// the old lifetime counter climbs, then the USDC cutover zeroes the unpaid part
	put(day("2030-10-27"), 15*1000*gb, 6000*gb)
	put(day("2030-11-03"), 15*1000*gb, 9000*gb) // Sunday: the anchor reads 24 TB
	put("2030-11-07T20:00:00Z", 15*1000*gb, 10_100*gb)
	put("2030-11-08T12:00:00Z", 15*1000*gb, 10_300*gb) // the last reading that still counts
	put("2030-11-08T12:15:00Z", 15*1000*gb, 0)         // the unpaid part is zeroed

	// epoch 5 (block 19) is inside the old counter already, so it must not be added again
	// epoch 6 closed after the anchor week and is added
	rows := []epochRow{
		{Epoch: 5, StartMillis: 1, EndMillis: time.Date(2030, 11, 8, 12, 0, 0, 0, time.UTC).UnixMilli()},
		{Epoch: 6, StartMillis: 2, EndMillis: time.Date(2030, 11, 15, 12, 0, 0, 0, time.UTC).UnixMilli()},
	}
	if err := storeEpochs(db, rows, time.Date(2030, 11, 25, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	db.Exec(`UPDATE epoch_stats SET served_bytes = ? WHERE epoch = 5`, 2700*gb)
	db.Exec(`UPDATE epoch_stats SET served_bytes = ? WHERE epoch = 6`, 3000*gb)

	// epoch 7 is open with nothing finished yet, and this week has 400 GB
	cur := &servedEpochWire{Epoch: 7, ServedByteCount: 0, AsOfMillis: servedT0.UnixMilli()}
	storeServedSample(db, blockAt(22, 400*gb, servedT0), cur, servedT0)
	p, err := buildServedPayload(db)
	if err != nil || p.Total == nil {
		t.Fatalf("payload = %+v, %v", p, err)
	}
	if p.Total.LegacyBytes != 24_000*gb {
		t.Errorf("old counter at the anchor = %d, want 24 TB", p.Total.LegacyBytes)
	}
	if want := time.Date(2030, 11, 3, 0, 0, 0, 0, time.UTC).UnixMilli(); p.Total.AnchorMillis != want {
		t.Errorf("anchor = %d, want the Sunday 2030-11-03", p.Total.AnchorMillis)
	}
	if want := 24_000*gb + 3000*gb + 400*gb; p.Total.Bytes != want {
		t.Errorf("total = %d, want %d (24 TB + epoch 6 + this week, epoch 5 not added)", p.Total.Bytes, want)
	}
}

func TestServedTotalWithoutAnOldCutoverIsTheServerFiguresAlone(t *testing.T) {
	db := servedTestDB(t)
	cur := &servedEpochWire{Epoch: 3, ServedByteCount: 250 * gb, AsOfMillis: servedT0.UnixMilli()}
	storeServedSample(db, blockAt(9, 40*gb, servedT0), cur, servedT0)
	p, _ := buildServedPayload(db)
	if p.Total == nil || p.Total.Bytes != 290*gb || p.Total.AnchorMillis != 0 {
		t.Fatalf("total = %+v, want 290 GB and no anchor", p.Total)
	}
}

func TestServedWeeksJoinTheOldCounterAndTheServerWithoutAGap(t *testing.T) {
	db := servedTestDB(t)
	put := func(at string, paid, unpaid int64) {
		db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?,?,?,?)", paid, unpaid, at, at)
	}
	put("2030-10-20T00:00:00Z", 15_000*gb, 4_000*gb)
	put("2030-10-27T00:00:00Z", 15_000*gb, 6_000*gb) // first week: 2000 GB
	put("2030-11-03T00:00:00Z", 15_000*gb, 8_700*gb) // second week: 2700 GB
	put("2030-11-08T12:00:00Z", 15_000*gb, 10_300*gb)
	put("2030-11-08T12:15:00Z", 15_000*gb, 0) // the cutover
	rows := []epochRow{{Epoch: 6, StartMillis: 2, EndMillis: time.Date(2030, 11, 15, 12, 0, 0, 0, time.UTC).UnixMilli()}}
	storeEpochs(db, rows, time.Date(2030, 11, 25, 0, 0, 0, 0, time.UTC))
	db.Exec(`UPDATE epoch_stats SET served_bytes = ? WHERE epoch = 6`, 3000*gb)
	cur := &servedEpochWire{Epoch: 7, ServedByteCount: 0, AsOfMillis: servedT0.UnixMilli()}
	blk := blockAt(22, 400*gb, servedT0)
	blk.StartMillis = time.Date(2030, 11, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	storeServedSample(db, blk, cur, servedT0)

	p, err := buildServedPayload(db)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	partial := map[string]bool{}
	for _, w := range p.Weeks {
		k := time.UnixMilli(w.StartMillis).UTC().Format("01-02")
		got[k], partial[k] = w.Bytes, w.Partial
	}
	want := map[string]int64{"10-20": 2000 * gb, "10-27": 2700 * gb, "11-03": 3000 * gb, "11-17": 400 * gb}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("week %s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	if !partial["11-17"] || partial["11-03"] {
		t.Errorf("partial flags wrong: %v", partial)
	}
	// weeks come in order
	for i := 1; i < len(p.Weeks); i++ {
		if p.Weeks[i].StartMillis <= p.Weeks[i-1].StartMillis {
			t.Fatalf("weeks out of order: %+v", p.Weeks)
		}
	}
}

func TestAlertSampleCarriesTheLatestRealInterval(t *testing.T) {
	t.Setenv("URWEBDASH_HOME", t.TempDir())
	postTimezone(t, `{"timezone":"America/Los_Angeles"}`)
	db := servedTestDB(t)
	a, err := buildAlertSample(db)
	if err != nil || a.OK {
		t.Fatalf("no samples = %+v, %v, want not ok", a, err)
	}
	storeServedSample(db, blockAt(1, 10*gb, servedT0), nil, servedT0)
	storeServedSample(db, blockAt(1, 18*gb, servedT0.Add(time.Hour)), nil, servedT0)
	a, err = buildAlertSample(db)
	if err != nil || !a.OK {
		t.Fatalf("sample = %+v, %v", a, err)
	}
	if a.DeltaGB != 8 || a.TotalGB != 18 || a.Span != "1h00m" || a.Clock != "03:00 PST" {
		t.Fatalf("sample = %+v, want 8 GB of 18 GB over 1h00m counted through 03:00 PST", a)
	}
	// the test message uses it, in the chosen style
	alertSampleSource = func() (alertSample, error) { return buildAlertSample(db) }
	t.Cleanup(func() { alertSampleSource = nil })
	body := sampleAlertBody("plain-lines")
	for _, want := range []string{"8.00 GB", "18.00 GB", "03:00 PST", "1h00m"} {
		if !strings.Contains(body, want) {
			t.Errorf("test message %q lacks %q", body, want)
		}
	}
}

func TestServedAnchorPrefersTheLastWeekBothCountersAgree(t *testing.T) {
	db := servedTestDB(t)
	put := func(at string, paid, unpaid int64) {
		db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?,?,?,?)", paid, unpaid, at, at)
	}
	// three Sundays: the first two weeks grow by 2000 and 2700 GB
	put("2030-10-20T00:00:00Z", 15_000*gb, 4_000*gb)
	put("2030-10-27T00:00:00Z", 15_000*gb, 6_000*gb)
	put("2030-11-03T00:00:00Z", 15_000*gb, 8_700*gb)
	// a one-off drop that is NOT the cutover week: it must not be what picks the join
	put("2030-11-08T12:00:00Z", 15_000*gb, 10_300*gb)
	put("2030-11-08T12:15:00Z", 15_000*gb, 0)
	rows := []epochRow{
		{Epoch: 4, StartMillis: 1, EndMillis: time.Date(2030, 11, 1, 12, 0, 0, 0, time.UTC).UnixMilli()},
		{Epoch: 5, StartMillis: 2, EndMillis: time.Date(2030, 11, 8, 12, 0, 0, 0, time.UTC).UnixMilli()},
		{Epoch: 6, StartMillis: 3, EndMillis: time.Date(2030, 11, 15, 12, 0, 0, 0, time.UTC).UnixMilli()},
	}
	storeEpochs(db, rows, time.Date(2030, 11, 25, 0, 0, 0, 0, time.UTC))
	db.Exec(`UPDATE epoch_stats SET served_bytes = ? WHERE epoch = 4`, 2000*gb)
	db.Exec(`UPDATE epoch_stats SET served_bytes = ? WHERE epoch = 5`, 2700*gb+gb/5) // a fraction of a GB apart
	db.Exec(`UPDATE epoch_stats SET served_bytes = ? WHERE epoch = 6`, 3000*gb)      // the old counter was zeroed: cannot agree

	pt, ok, err := servedAnchor(db)
	if err != nil || !ok {
		t.Fatalf("anchor = %+v %v %v", pt, ok, err)
	}
	if want := time.Date(2030, 11, 3, 0, 0, 0, 0, time.UTC).UnixMilli(); pt.BlockStart != want || pt.Method != anchorAgree {
		t.Fatalf("anchor = %+v, want the Sunday 2030-11-03 by agreement", pt)
	}
	if pt.LegacyBytes != 23_700*gb {
		t.Fatalf("old counter at the anchor = %d, want 23.7 TB", pt.LegacyBytes)
	}
}

func TestServedAnchorRefusesAWeekWhereTheCountersDisagree(t *testing.T) {
	db := servedTestDB(t)
	db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(0, ?, '2030-10-27T00:00:00Z', 'x')", 1000*gb)
	db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(0, ?, '2030-11-03T00:00:00Z', 'x')", 3000*gb) // +2000 GB
	storeEpochs(db, []epochRow{{Epoch: 5, StartMillis: 1, EndMillis: time.Date(2030, 11, 8, 12, 0, 0, 0, time.UTC).UnixMilli()}}, time.Date(2030, 11, 25, 0, 0, 0, 0, time.UTC))
	db.Exec(`UPDATE epoch_stats SET served_bytes = ? WHERE epoch = 5`, 5000*gb) // the server says 5000
	if pt, ok, _ := servedAnchor(db); ok {
		t.Fatalf("joined on a week that disagrees by 3 TB: %+v", pt)
	}
}

func TestArchiveEndsOneRowAfterTheTransition(t *testing.T) {
	resetArchiveEnd()
	t.Cleanup(resetArchiveEnd)
	db := servedTestDB(t)
	put := func(at string, paid, unpaid int64) {
		db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?,?,?,?)", paid, unpaid, at, at)
	}
	put("2030-11-08T14:30:00Z", 15_000*gb, 10_000*gb)
	put("2030-11-08T14:45:00Z", 15_000*gb, 0) // the transition
	put("2030-11-08T15:00:00Z", 15_000*gb, 0) // one row after: kept
	put("2030-11-08T15:15:00Z", 15_000*gb, 0)
	put("2030-11-09T15:15:00Z", 15_000*gb, 0)
	if got := archiveEnd(db); got != "2030-11-08T15:00:00Z" {
		t.Fatalf("archive ends at %q, want one row after the transition", got)
	}
	rec := httptest.NewRecorder()
	handleWalletStats(db)(rec, httptest.NewRequest("GET", "/api/wallet-stats", nil))
	var out struct {
		Entries []struct {
			CreatedAt string `json:"created_at"`
		} `json:"entries"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 3 || len(out.Entries) != 3 || out.Entries[0].CreatedAt != "2030-11-08T15:00:00Z" {
		t.Fatalf("archive rows = %+v (total %d), want the three rows up to the cut, newest first", out.Entries, out.Total)
	}
	var stored int
	db.QueryRow(`SELECT count(*) FROM wallet_stats`).Scan(&stored)
	if stored != 5 {
		t.Fatalf("rows in the database = %d, want all 5 kept", stored)
	}
}

// The poller and the server start at once on one database. Both see a column
// missing and both add it. The loser must not fail the whole start.
func TestAddColumnIfMissingSurvivesAConcurrentAdd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "race.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close() })
		return db
	}
	first := open()
	if _, err := first.Exec(`CREATE TABLE race (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 25; round++ {
		col := fmt.Sprintf("c%d", round)
		errs := make(chan error, 6)
		start := make(chan struct{})
		for i := 0; i < 6; i++ {
			db := open()
			go func() {
				<-start
				errs <- addColumnIfMissing(db, "race", col, "INTEGER")
			}()
		}
		close(start)
		for i := 0; i < 6; i++ {
			if err := <-errs; err != nil && !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "busy") {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
}

func TestWindowWordsAndTheSpikeWindowFollowTheInterval(t *testing.T) {
	for in, want := range map[time.Duration]string{time.Hour: "hour", 2 * time.Hour: "2 hours", 15 * time.Minute: "15 minutes", 90 * time.Minute: "90 minutes"} {
		if got := windowWords(in); got != want {
			t.Errorf("windowWords(%v) = %q, want %q", in, got, want)
		}
	}
	t.Setenv("STATS_INTERVAL", "")
	if servedSpikeWindow() != time.Hour {
		t.Errorf("default spike window = %v, want 1h", servedSpikeWindow())
	}
	t.Setenv("STATS_INTERVAL", "15m")
	if servedSpikeWindow() != 15*time.Minute {
		t.Errorf("spike window with STATS_INTERVAL=15m = %v", servedSpikeWindow())
	}
}

func TestSpikeThresholdIsPerPollWindow(t *testing.T) {
	srv, closeSrv := newSpikeCountServer()
	defer closeSrv()
	t.Setenv("DISCORD_WEBHOOK_URL", srv.URL)
	t.Setenv("SPIKE_THRESHOLD", "3GB")
	db := servedTestDB(t)
	storeServedSample(db, blockAt(1, 10*gb, servedT0), nil, servedT0)
	storeServedSample(db, blockAt(1, 18*gb, servedT0.Add(time.Hour)), nil, servedT0) // 8 GB in one hour

	// a one-hour window: 8 GB per window is over 3 GB
	t.Setenv("STATS_INTERVAL", "1h")
	checkServedSpike(db, servedT0.Add(time.Hour).UnixMilli(), servedT0)
	for i := 0; i < 50 && srv.count() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.count() != 1 {
		t.Fatalf("hourly window: %d alerts, want 1", srv.count())
	}
	// a 15-minute window: the same hour is 2 GB per window, under 3 GB
	t.Setenv("STATS_INTERVAL", "15m")
	checkServedSpike(db, servedT0.Add(time.Hour).UnixMilli(), servedT0)
	time.Sleep(60 * time.Millisecond)
	if srv.count() != 1 {
		t.Fatalf("15 minute window: %d alerts, want still 1", srv.count())
	}
}

// A small account moves a few gigabytes a week. The rules must not assume a
// terabyte network: a fixed tolerance of a few GB would accept any week.
func TestServedAnchorFitsASmallAccount(t *testing.T) {
	db := servedTestDB(t)
	mb := int64(1_000_000)
	put := func(at string, total int64) {
		db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?,0,?,?)", total, at, at)
	}
	put("2030-11-03T00:00:00Z", 10_000*mb)
	put("2030-11-10T00:00:00Z", 13_000*mb) // grew 3 GB
	end := time.Date(2030, 11, 15, 12, 0, 0, 0, time.UTC).UnixMilli()
	storeEpochs(db, []epochRow{{Epoch: 5, StartMillis: 1, EndMillis: end}}, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC))

	db.Exec(`UPDATE epoch_stats SET served_bytes = ? WHERE epoch = 5`, 3_010*mb) // within 1 percent
	if pt, ok, err := servedAnchor(db); err != nil || !ok || pt.Method != anchorAgree {
		t.Fatalf("a 3 GB week that matches within 1%% was not joined: %+v %v %v", pt, ok, err)
	}

	db2 := servedTestDB(t)
	db2.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(10000000000,0,'2030-11-03T00:00:00Z','x')")
	db2.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(13000000000,0,'2030-11-10T00:00:00Z','x')")
	storeEpochs(db2, []epochRow{{Epoch: 5, StartMillis: 1, EndMillis: end}}, time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC))
	db2.Exec(`UPDATE epoch_stats SET served_bytes = ? WHERE epoch = 5`, 5_000*mb) // 2 GB apart on a 3 GB week
	if pt, ok, _ := servedAnchor(db2); ok && pt.Method == anchorAgree {
		t.Fatalf("a 3 GB week that is 2 GB apart was accepted as agreeing: %+v", pt)
	}
}

func TestDropRuleFitsASmallAccount(t *testing.T) {
	resetArchiveEnd()
	t.Cleanup(resetArchiveEnd)
	db := servedTestDB(t)
	put := func(at string, total int64) {
		db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?,0,?,?)", total, at, at)
	}
	gbi := int64(1_000_000_000)
	put("2030-11-10T10:00:00Z", 80*gbi)
	put("2030-11-10T11:00:00Z", 50*gbi) // a 37 percent drop on a small total
	put("2030-11-10T12:00:00Z", 50*gbi)
	put("2030-11-10T13:00:00Z", 50*gbi)
	if got := archiveEnd(db); got != "2030-11-10T12:00:00Z" {
		t.Fatalf("archive end for a small account = %q, want one row after the drop", got)
	}
	// a small wobble is not a transition
	resetArchiveEnd()
	db3 := servedTestDB(t)
	db3.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(100000000000,0,'2030-11-10T10:00:00Z','x')")
	db3.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(99000000000,0,'2030-11-10T11:00:00Z','x')") // minus 1 percent
	if got := archiveEnd(db3); got != "" {
		t.Fatalf("a 1%% correction was taken for the transition: %q", got)
	}
}

// If the transition is the newest row, the row after it does not exist yet.
// The cut must not be cached, or that row would stay hidden for good.
func TestArchiveCutWaitsForTheRowAfterTheTransition(t *testing.T) {
	resetArchiveEnd()
	t.Cleanup(resetArchiveEnd)
	db := servedTestDB(t)
	put := func(at string, total int64) {
		db.Exec("INSERT INTO wallet_stats(paid_bytes, unpaid_bytes, created_at, updated_at) VALUES(?,0,?,?)", total, at, at)
	}
	put("2030-11-10T10:00:00Z", 100*gb)
	put("2030-11-10T11:00:00Z", 60*gb) // the transition is the newest row
	if got := archiveEnd(db); got != "2030-11-10T11:00:00Z" {
		t.Fatalf("with no row after the transition the cut = %q, want the transition row", got)
	}
	put("2030-11-10T12:00:00Z", 60*gb)
	if got := archiveEnd(db); got != "2030-11-10T12:00:00Z" {
		t.Fatalf("after the next row arrived the cut = %q, want that row, the cut was cached too early", got)
	}
}

// A short poll interval must not shorten the history the page gets.
func TestServedHistoryIsATimeSpanNotASampleCount(t *testing.T) {
	db := servedTestDB(t)
	t0 := servedT0
	for i := 0; i < 4*24*3; i++ { // three days of 15-minute samples
		storeServedSample(db, blockAt(1, int64(i)*gb/10, t0.Add(time.Duration(i)*15*time.Minute)), nil, t0)
	}
	p, err := buildServedPayload(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Intervals) < 4*24*3-2 {
		t.Fatalf("intervals = %d, want about three days of them (288), not a fixed 192", len(p.Intervals))
	}
}

func TestLast24hRefusesABaselineNowhereNearADayOld(t *testing.T) {
	db := servedTestDB(t)
	storeServedSample(db, blockAt(1, 10*gb, servedT0), nil, servedT0)                     // five days earlier
	storeServedSample(db, blockAt(1, 90*gb, servedT0.Add(5*24*time.Hour)), nil, servedT0) // newest
	p, _ := buildServedPayload(db)
	if p.Last24h != nil {
		t.Fatalf("a baseline five days old was shown as the last 24 hours: %+v", p.Last24h)
	}
	storeServedSample(db, blockAt(1, 70*gb, servedT0.Add(5*24*time.Hour-25*time.Hour)), nil, servedT0) // 25 hours before the newest
	p, _ = buildServedPayload(db)
	if p.Last24h == nil || p.Last24h.Bytes != 20*gb {
		t.Fatalf("a baseline 25 hours old = %+v, want 20 GB", p.Last24h)
	}
}

func TestTestMessageWithNoSamplesUsesTheConfiguredWindow(t *testing.T) {
	t.Setenv("URWEBDASH_HOME", t.TempDir())
	t.Setenv("STATS_INTERVAL", "")
	db := servedTestDB(t)
	alertSampleSource = func() (alertSample, error) { return buildAlertSample(db) }
	t.Cleanup(func() { alertSampleSource = nil })
	body := sampleAlertBody("plain-lines")
	if !strings.Contains(body, "1h00m") || strings.Contains(body, "15m") {
		t.Fatalf("test message with no samples = %q, want the one-hour window and no 15m", body)
	}
	alertSampleSource = nil
	if body := sampleAlertBody("plain-lines"); strings.Contains(body, "15m") {
		t.Fatalf("test message with no source = %q, still says 15m", body)
	}
}
