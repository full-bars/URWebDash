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

// funcBody returns a whole top-level function, brace-matched from its signature.
// A rename or a moved body fails loudly instead of silently testing nothing,
// which is what a fixed end-marker would do. Braces inside string and template
// literals are skipped so a message like "{" cannot truncate the body.
func funcBody(t *testing.T, page, sig string) string {
	t.Helper()
	i := strings.Index(page, sig)
	if i < 0 {
		t.Fatalf("index.html is missing %q", sig)
	}
	depth := 0
	var quote byte
	for j := i; j < len(page); j++ {
		c := page[j]
		if quote != 0 {
			if c == '\\' {
				j++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return page[i : j+1]
			}
		}
	}
	t.Fatalf("%q is not brace-balanced", sig)
	return ""
}

// pageFormatters returns the GB/time helpers with the zone pinned, so an
// assertion does not depend on the runner's clock.
func pageFormatters(t *testing.T, page string) string {
	t.Helper()
	return strings.Replace(
		sliceBetween(t, page, "const GB=1e9,TB=1e12;", "const series="),
		"const TZ=Intl.DateTimeFormat().resolvedOptions().timeZone;",
		"const TZ='America/Los_Angeles';", 1)
}

func pageDayCards(t *testing.T, page string) string {
	t.Helper()
	return sliceBetween(t, page, "const asRows=rows=>",
		"const tbody=document.getElementById('history-table');\nrecent.forEach")
}

// pageInitialPaint returns the first paint's block: the day cards, the weekly
// tiles and the recent-row loop that runs straight from the embedded snapshot.
func pageInitialPaint(t *testing.T, page string) string {
	t.Helper()
	return sliceBetween(t, page, "const series=", "const payTable=")
}

func runNode(t *testing.T, node, harness string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "page_logic.js")
	if err := os.WriteFile(script, []byte(harness), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	cmd := exec.Command(node, script)
	// dayStartMs uses the local date getters, so pin the process zone as well as
	// the page's TZ constant or the day-card assertions pass only on a Pacific
	// machine and fail on a UTC CI runner.
	cmd.Env = append(os.Environ(), "TZ=America/Los_Angeles")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(out), "FAIL") {
		t.Fatalf("page logic failed:\n%s", out)
	}
}

func readPage(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	// A CRLF checkout must not turn every marker lookup into a missing-marker
	// failure.
	return strings.ReplaceAll(string(src), "\r\n", "\n")
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
	page := readPage(t)

	harness := pageFormatters(t, page) + pageDayCards(t, page) + `
let fail = 0;
const eq = (n, got, want) => {
  if (String(got) !== String(want)) { fail++; console.log('FAIL ' + n + ': got ' + JSON.stringify(got) + ' want ' + JSON.stringify(want)); }
};

// A normal fetch lands within seconds of its window and must not be annotated.
eq('lag 0s', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:45:00Z'), '');
eq('lag 20s', lagNote('2026-11-02T21:15:00Z', '2026-11-02T21:15:20Z'), '');
eq('lag 119s', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:46:59Z'), '');
// The gate is inclusive: exactly two minutes is still a normal fetch.
eq('lag exactly 120s', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:47:00Z'), '');
// One second past it is a retry, and the note is in local time.
eq('lag 2m1s', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:47:01Z'), ' \u00b7 fetched 01:47 PM (+2m)');
eq('lag 5m', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:50:00Z'), ' \u00b7 fetched 01:50 PM (+5m)');
// A clock that steps backwards must not print a negative lag as if it were one.
eq('lag negative', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:40:00Z'), '');
eq('lag missing updated_at', lagNote('2026-11-02T21:45:00Z', ''), '');

// The poller writes UTC; the page shows the reader's clock.
eq('fmtTime UTC->PT', fmtTime('2026-11-02T21:45:00Z'), 'Nov 2, 2026, 1:45:00 PM');
eq('fmtTime crosses midnight', fmtTime('2026-11-03T07:00:00Z'), 'Nov 2, 2026, 11:00:00 PM');
// Both sides of the DST fall-back: 08:30Z and 09:30Z are 01:30 local, an hour apart.
eq('DST before', fmtTime('2026-11-01T08:30:00Z'), 'Nov 1, 2026, 1:30:00 AM');
eq('DST after', fmtTime('2026-11-01T09:30:00Z'), 'Nov 1, 2026, 1:30:00 AM');
// A stored value that is not a date must not render as "Invalid Date".
eq('fmtTime junk', fmtTime('not-a-date'), 'not-a-date');

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
// Local midnight (08:00Z on Nov 2 in Pacific) is the baseline, so the day runs
// from that sample rather than from the 07:45 one before it.
eq('today value', (asc.today / 1e9).toFixed(2), '287.91');

// The boundary sample itself is a valid baseline. A node whose history starts
// exactly at local midnight must show its real growth, not a dash.
const fromMidnight = [
  { created_at: '2026-11-02T12:00:00Z', paid_bytes: 0, unpaid_bytes: 6000000000000 },
  { created_at: '2026-11-02T08:00:00Z', paid_bytes: 0, unpaid_bytes: 5000000000000 },
];
eq('midnight sample is the baseline', paintDayCards(asRows(fromMidnight)).today, 1000000000000);

// With no sample at or before local midnight there is no day figure at all;
// the difference would otherwise be the lifetime total.
const onlyToday = full.filter(r => new Date(r.created_at) > new Date('2026-11-02T08:00:00Z'));
eq('no pre-midnight sample', paintDayCards(asRows(onlyToday)).today, null);

if (fail) { console.log(fail + ' assertion(s) failed'); process.exit(1); }
`

	runNode(t, node, harness)
}

// TestHistoryRenderPathsAgree pins the bug class itself: the paged renderer and
// the live-poll inserter must produce the same cells for the same row. They did
// not, and that is what made a row change format twenty seconds after load.
func TestHistoryRenderPathsAgree(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping page-logic test")
	}
	page := readPage(t)

	harness := pageFormatters(t, page) +
		funcBody(t, page, "function renderWalletTable(){") +
		funcBody(t, page, "function epInsertRow(r, raw){") + `
let fail = 0;
const eq = (n, got, want) => {
  if (String(got) !== String(want)) { fail++; console.log('FAIL ' + n + ': got ' + JSON.stringify(got) + ' want ' + JSON.stringify(want)); }
};
const cells = html => (html.match(/<td[^>]*>([\s\S]*?)<\/td>/g) || []).map(c => c.replace(/<[^>]+>/g, '').trim());

// A DOM stub that collects the rows each render path produces.
function makeDom() {
  const rows = [];
  const tbody = {
    dataset: {}, innerHTML: '', firstChild: null, rows,
    appendChild(n) { if (n && n.children) rows.push(...n.children); else rows.push(n); },
    insertBefore(n) { rows.unshift(n); },
    deleteRow() { rows.pop(); },
  };
  const generic = () => ({ innerHTML: '', className: '', textContent: '', style: {}, children: [],
                           appendChild(n) { this.children.push(n); },
                           querySelector: () => null,
                           classList: { contains: () => false, add() {}, remove() {} } });
  const pager = generic();
  const doc = {
    getElementById: id => (id === 'history-table' ? tbody : id === 'table-pagination' ? pager : generic()),
    createElement: () => ({ innerHTML: '', className: '', textContent: '', id: '', disabled: false, onclick: null }),
    createDocumentFragment: () => ({ children: [], appendChild(n) { this.children.push(n); } }),
  };
  global.document = doc;
  return { tbody, rows };
}

// The same window, fetched seven minutes late, and its on-time predecessor.
const row = { created_at: '2026-11-02T21:45:00Z', updated_at: '2026-11-02T21:52:00Z',
              paid_bytes: 15871330000000, unpaid_bytes: 6128119780368, change_bytes: 4571713707 };
const older = { created_at: '2026-11-02T21:30:00Z', updated_at: '2026-11-02T21:30:00Z',
                paid_bytes: 15871330000000, unpaid_bytes: 6123548066661, change_bytes: 5370523725 };

// ---- path 1: the server-embedded snapshot, painted before any fetch lands ----
// It carries its own "fetched" field rather than updated_at.
const HIST = {
  series: [
    { t: older.created_at, paid: older.paid_bytes, unpaid: older.unpaid_bytes },
    { t: row.created_at, paid: row.paid_bytes, unpaid: row.unpaid_bytes },
  ],
  recent: [
    { t: row.created_at, fetched: row.updated_at, paid: row.paid_bytes, unpaid: row.unpaid_bytes },
    { t: older.created_at, fetched: older.updated_at, paid: older.paid_bytes, unpaid: older.unpaid_bytes },
  ],
};
const dom1 = makeDom();
` + pageInitialPaint(t, page) + `
const initialCells = cells(dom1.rows[0].innerHTML);

// ---- path 2: the paged render from /api/wallet-stats ----
global.PAGE_SIZE = 50;
global.walletPage = 1;
global.WALLETROWS = [older, row].slice().reverse();
const a = makeDom();
renderWalletTable();
const pagedCells = cells(a.rows[0].innerHTML);

// ---- path 3: the row arriving on the twenty-second live poll ----
const b = makeDom();
epInsertRow(row, [row, older]);
const liveCells = cells(b.rows[0].innerHTML);

eq('cell count', liveCells.length, 7);
for (const [name, i] of [['timestamp',0],['paid',1],['unpaid bytes',2],['change bytes',3],['unpaid GB',4],['change GB',5],['gb per hour',6]]) {
  eq('initial==paged ' + name, initialCells[i], pagedCells[i]);
  eq('initial==live ' + name, initialCells[i], liveCells[i]);
  eq('paged==live ' + name, pagedCells[i], liveCells[i]);
}

// The row really is late, so all three paths must carry the note.
for (const [name, cs] of [['initial', initialCells], ['paged', pagedCells], ['live', liveCells]]) {
  if (!/\+7m/.test(cs[0])) { fail++; console.log('FAIL late note missing on ' + name + ': ' + JSON.stringify(cs[0])); }
}

// An on-time row must carry no note on any path.
const c = makeDom();
global.WALLETROWS = [older];
renderWalletTable();
const onTime = cells(c.rows[0].innerHTML)[0];
if (/\u00b7 fetched/.test(onTime)) {
  fail++;
  console.log('FAIL on-time row annotated: ' + JSON.stringify(onTime));
}

// A rollback must read as "-5.00", never "+-5.00", and keep its separators.
const negative = { created_at: '2026-11-02T22:00:00Z', updated_at: '2026-11-02T22:00:00Z',
                   paid_bytes: 15871330000000, unpaid_bytes: 6123119780368, change_bytes: -5000000000 };
global.WALLETROWS = [negative, older];
const neg = makeDom();
renderWalletTable();
const negCells = cells(neg.rows[0].innerHTML);
eq('negative change bytes', negCells[3], '-5,000,000,000');
eq('negative change GB', negCells[5], '-5.00');
// thirty minutes since the previous window, so the hourly rate doubles the gap
eq('negative gb per hour', negCells[6], '-10.00');

if (fail) { console.log(fail + ' assertion(s) failed'); process.exit(1); }
`

	runNode(t, node, harness)
}

// A window whose total went DOWN (a rollback or a salvage merge) must print a
// single minus, not "+-". The first paint had a hardcoded '+' on the delta, so
// this pins that path specifically instead of assuming the paged one covers it.
func TestHistoryNegativeDeltaIsSigned(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping page-logic test")
	}
	page := readPage(t)

	harness := pageFormatters(t, page) + `
let fail = 0;
const eq = (n, got, want) => {
  if (String(got) !== String(want)) { fail++; console.log('FAIL ' + n + ': got ' + JSON.stringify(got) + ' want ' + JSON.stringify(want)); }
};
const cells = html => (html.match(/<td[^>]*>([\s\S]*?)<\/td>/g) || []).map(c => c.replace(/<[^>]+>/g, '').trim());

// the block below declares its own "tbody" binding, so this stub stays separate
const rows = [];
const tbodyStub = {
  dataset: {}, innerHTML: '', firstChild: null, rows,
  appendChild(n) { if (n && n.children) rows.push(...n.children); else rows.push(n); },
  insertBefore(n) { rows.unshift(n); }, deleteRow() { rows.pop(); },
};
const generic = () => ({ innerHTML: '', className: '', textContent: '', style: {}, children: [],
                         appendChild(n) { this.children.push(n); }, querySelector: () => null,
                         classList: { contains: () => false, add() {}, remove() {} } });
const pager = generic();
global.document = {
  getElementById: id => (id === 'history-table' ? tbodyStub : id === 'table-pagination' ? pager : generic()),
  createElement: () => ({ innerHTML: '', className: '', textContent: '', id: '', disabled: false, onclick: null }),
  createDocumentFragment: () => ({ children: [], appendChild(n) { this.children.push(n); } }),
};

// Total fell by exactly 5 GB over thirty minutes.
const HIST = {
  series: [
    { t: '2026-11-02T21:30:00Z', paid: 0, unpaid: 6128119780368 },
    { t: '2026-11-02T22:00:00Z', paid: 0, unpaid: 6123119780368 },
  ],
  recent: [
    { t: '2026-11-02T22:00:00Z', fetched: '2026-11-02T22:00:00Z', paid: 0, unpaid: 6123119780368 },
    { t: '2026-11-02T21:30:00Z', fetched: '2026-11-02T21:30:00Z', paid: 0, unpaid: 6128119780368 },
  ],
};
` + pageInitialPaint(t, page) + `
const c = cells(rows[0].innerHTML);
eq('cell count', c.length, 7);
eq('negative change bytes', c[3], '-5,000,000,000');
eq('negative change GB', c[5], '-5.00');
eq('negative gb per hour', c[6], '-10.00');

if (fail) { console.log(fail + ' assertion(s) failed'); process.exit(1); }
`

	runNode(t, node, harness)
}
