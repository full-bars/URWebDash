package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sliceBetween returns the text of page from start up to (not including) end.
func sliceBetween(t *testing.T, page, start, end string) string {
	t.Helper()
	i := strings.Index(page, start)
	if i < 0 {
		t.Fatalf("index.html is missing %q", start)
	}
	j := strings.Index(page[i:], end)
	if j < 0 {
		t.Fatalf("index.html is missing %q after %q", end, start)
	}
	return page[i : i+j]
}

// The history table is painted by three render paths in index.html, and they
// used to disagree: one printed whole GB, another two decimals, and the
// late-fetch note had no threshold so every row claimed a retry. These
// assertions run the shipped helpers through node, so the page's real source is
// what gets tested rather than a copy of it.
func TestHistoryRowFormatting(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping page-logic test")
	}
	src, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	page := string(src)

	formatters := sliceBetween(t, page, "const GB=1e9,TB=1e12;", "const esc=s=>")
	cards := sliceBetween(t, page, "const asRows=rows=>",
		"const tbody=document.getElementById('history-table');\nrecent.forEach")

	// Pin the zone so the assertion does not depend on the runner's clock.
	formatters = strings.Replace(formatters,
		"const TZ=Intl.DateTimeFormat().resolvedOptions().timeZone;",
		"const TZ='America/Los_Angeles';", 1)

	harness := formatters + cards + `
let fail = 0;
const eq = (n, got, want) => {
  if (String(got) !== String(want)) { fail++; console.log('FAIL ' + n + ': got ' + JSON.stringify(got) + ' want ' + JSON.stringify(want)); }
};

// A normal fetch lands within seconds of its window and must not be annotated.
eq('lag 0s', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:45:00Z'), '');
eq('lag 20s', lagNote('2026-11-02T21:15:00Z', '2026-11-02T21:15:20Z'), '');
eq('lag 119s', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:46:59Z'), '');
// A window the poller had to retry past its boundary is annotated, in local time.
eq('lag 2m1s', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:47:01Z'), ' \u00b7 fetched 01:47 PM (+2m)');
eq('lag 5m', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:50:00Z'), ' \u00b7 fetched 01:50 PM (+5m)');
eq('lag missing updated_at', lagNote('2026-11-02T21:45:00Z', ''), '');

// The poller writes UTC; the page shows the reader's clock.
eq('fmtTime UTC->PT', fmtTime('2026-11-02T21:45:00Z'), '2026-11-02 13:45:00');
eq('fmtTime crosses midnight', fmtTime('2026-11-03T07:00:00Z'), '2026-11-02 23:00:00');

// One GB format everywhere.
eq('fmtGB2', fmtGB2(15871330000000), '15,871.33');
eq('fmtGB alias', fmtGB(6128119780368), '6,128.12');
eq('fmtBytes', fmtBytes(387040000000), '387.04 GB');

// The day cards must not depend on the order the API returns rows in: the
// initial render gets ASC, the live poll sorts DESC.
const full = [];
for (let i = 0; i < 96; i++) {
  const t = new Date(Date.UTC(2026, 10, 2, 0, 0, 0) + i * 15 * 60000).toISOString();
  full.push({ created_at: t, paid_bytes: 1000000000000, unpaid_bytes: 5000000000000 + i * 4570000000 });
}
const stub = { textContent: '', className: '', querySelector: () => ({ textContent: '', className: '' }) };
global.document = { getElementById: () => stub };
const asc = paintDayCards(asRows(full));
const desc = paintDayCards(asRows(full.slice().reverse()));
eq('today ASC==DESC', asc.today, desc.today);
eq('24h ASC==DESC', asc.day24, desc.day24);
eq('today value', (asc.today / 1e9).toFixed(2), '292.48');

// Without a sample from before local midnight there is no day figure at all;
// the difference would otherwise be the lifetime total.
const onlyToday = full.filter(r => new Date(r.created_at) >= new Date('2026-11-02T08:00:00Z'));
eq('no pre-midnight sample', paintDayCards(asRows(onlyToday)).today, null);

if (fail) { console.log(fail + ' assertion(s) failed'); process.exit(1); }
`

	dir := t.TempDir()
	script := filepath.Join(dir, "history_format.js")
	if err := os.WriteFile(script, []byte(harness), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	out, err := exec.Command(node, script).CombinedOutput()
	if err != nil {
		t.Fatalf("page logic failed:\n%s", out)
	}
	if strings.Contains(string(out), "FAIL") {
		t.Fatalf("page logic failed:\n%s", out)
	}
}
