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

	harness := pageFormatters(t, page) + `
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
eq('lag 2m1s', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:47:01Z'), ' <span class="text-yellow">\u00b7 fetched 01:47 PM (+2m)</span>');
eq('lag 5m', lagNote('2026-11-02T21:45:00Z', '2026-11-02T21:50:00Z'), ' <span class="text-yellow">\u00b7 fetched 01:50 PM (+5m)</span>');
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

// A duplicate timestamp must not divide by an epsilon and inflate the rate.
eq('windowHours 15m', windowHours('2026-11-02T21:45:00Z', '2026-11-02T21:30:00Z'), 0.25);
eq('windowHours duplicate', windowHours('2026-11-02T21:45:00Z', '2026-11-02T21:45:00Z'), 0.25);
eq('windowHours backwards', windowHours('2026-11-02T21:45:00Z', '2026-11-02T22:00:00Z'), 0.25);
eq('windowHours no prev', windowHours('2026-11-02T21:45:00Z', undefined), 0.25);

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

// The webhook card must show what is actually saved, and Save must never treat
// a blank box as "clear the stored URL" — that is how clicking Save used to
// silently delete a working webhook.
func TestWebhookCardShowsSavedState(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping page-logic test")
	}
	page := readPage(t)

	harness := pageFormatters(t, page) +
		funcBody(t, page, "async function loadWebhook(){") +
		funcBody(t, page, "async function whSave(btn){") + `
let fail = 0;
const eq = (n, got, want) => {
  if (String(got) !== String(want)) { fail++; console.log('FAIL ' + n + ': got ' + JSON.stringify(got) + ' want ' + JSON.stringify(want)); }
};
const has = (n, hay, needle) => {
  if (String(hay).indexOf(needle) < 0) { fail++; console.log('FAIL ' + n + ': ' + JSON.stringify(hay) + ' is missing ' + JSON.stringify(needle)); }
};

const els = {};
const el = id => (els[id] = els[id] || { id: id, innerHTML: '', textContent: '', value: '', placeholder: '', focus() {} });
global.document = { getElementById: id => el(id) };

let state;
global.fetch = async () => ({ json: async () => state });

// 1. a saved webhook reads as configured, masked, and names its source
state = { configured: true, source: 'file', url_masked: 'https://discord.com/api/webhooks/1234/abcd…' };
let whConfigured = false;
(async () => {
  await loadWebhook();
  const st = el('wh-state').innerHTML;
  has('configured state', st, 'Configured');
  has('masked url shown', st, 'abcd…');
  has('source shown', st, 'saved on this host');
  eq('placeholder offers replace', el('wh-url').placeholder, 'paste a new URL to replace the saved one');
  eq('whConfigured flag', whConfigured, true);

  // 2. nothing saved reads as not configured
  state = { configured: false, source: '' };
  await loadWebhook();
  has('unconfigured state', el('wh-state').innerHTML, 'Not configured');
  eq('placeholder offers set', el('wh-url').placeholder, 'paste a Discord webhook URL to start alerts');
  eq('whConfigured flag cleared', whConfigured, false);

  // 3. Save with a blank box must NOT post — an empty body clears the URL
  state = { configured: true, source: 'file', url_masked: 'https://discord.com/api/webhooks/1234/abcd…' };
  await loadWebhook();
  let posts = 0;
  const before = global.fetch;
  global.fetch = async (u, o) => { if (o && o.method === 'POST') posts++; return { json: async () => ({}) }; };
  const btn = { disabled: false, innerHTML: 'Save' };
  el('wh-url').value = '   ';
  await whSave(btn);
  eq('blank save did not post', posts, 0);
  has('blank save explains why', el('wh-note').textContent, 'already saved');
  eq('blank save re-enables the button', btn.disabled, false);

  // 4. a real URL does post exactly what was typed
  let sent = null;
  global.fetch = async (u, o) => {
    if (o && o.method === 'POST') { posts++; sent = JSON.parse(o.body).url; }
    return { ok: true, json: async () => ({ ok: true, configured: true }) };
  };
  el('wh-url').value = '  https://discord.com/api/webhooks/99/real  ';
  await whSave(btn);
  eq('typed url posted trimmed', sent, 'https://discord.com/api/webhooks/99/real');
  eq('entry box cleared after save', el('wh-url').value, '');

  global.fetch = before;
  if (fail) { console.log(fail + ' assertion(s) failed'); process.exit(1); }
})().catch(e => { console.log('FAIL threw: ' + (e && e.message)); process.exit(1); });
`

	runNode(t, node, harness)
}
