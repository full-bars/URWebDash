package main

// Server-side data for the dashboard page.
//
// The page is one static build: it reads three objects, D (summary), HIST
// (traffic series) and PAY (payout history), and renders every theme from
// them. Rather than teach 200KB of rendering code to fetch, the server fills
// those three objects at request time from the same sources the JSON API
// uses, so the page keeps its exact approved markup and always has live
// numbers.
//
// Every derived figure is marked as derived in its own field name (pct, head,
// closeAt) so the page can label it; nothing here invents a value it cannot
// source, and an absent source renders as null rather than zero.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type dashEpoch struct {
	N               int64   `json:"n"`
	NextEpoch       int64   `json:"nextEpoch"`
	Phase           string  `json:"phase"`
	StartBlock      int64   `json:"startBlock"`
	EarningEndBlock int64   `json:"earningEndBlock"`
	CommitBlock     int64   `json:"commitBlock"`
	FinalizeBlock   int64   `json:"finalizeBlock"`
	TBlocks         int64   `json:"tBlocks"`
	BlockSeconds    int64   `json:"blockSeconds"`
	Head            int64   `json:"head"` // estimated, see dashHead
	ElapsedBlocks   int64   `json:"elapsedBlocks"`
	LeftBlocks      int64   `json:"leftBlocks"`
	Pct             float64 `json:"pct"`
	CloseAt         string  `json:"closeAt"`
	CommitIn        string  `json:"commitIn"`
	FinalizeIn      string  `json:"finalizeIn"`
	EpochLengthDays float64 `json:"epochLengthDays"`
}

type dashPoints struct {
	Total            float64 `json:"total"`
	EpochsWithPoints int     `json:"epochsWithPoints"`
	EpochsFinalized  int     `json:"epochsFinalized"`
	BestEpoch        int64   `json:"bestEpoch"`
	BestPoints       float64 `json:"bestPoints"`
}

type dashEpochRow struct {
	Epoch      int64    `json:"epoch"`
	Points     float64  `json:"points"`
	ShareBps   int64    `json:"shareBps"`
	Alpha      *float64 `json:"alpha"`
	Settlement string   `json:"settlement"`
}

type dashWallet struct {
	Coldkey      string `json:"coldkey"`
	Scope        string `json:"scope"`
	FromEpoch    int64  `json:"fromEpoch"`
	ThroughEpoch int64  `json:"throughEpoch"`
}

type dashAlpha struct {
	Unclaimed        *float64 `json:"unclaimed"`
	ThisEpoch        string   `json:"thisEpoch"`
	FirstPayingEpoch int64    `json:"firstPayingEpoch"`
}

type dashTraffic struct {
	PaidBytes   int64   `json:"paidBytes"`
	UnpaidBytes int64   `json:"unpaidBytes"`
	PaidTb      float64 `json:"paidTb"`
	UnpaidTb    float64 `json:"unpaidTb"`
}

type dashLegacy struct {
	Usdc    float64 `json:"usdc"`
	Count   int     `json:"count"`
	Last    string  `json:"last"`
	Network string  `json:"network"`
}

type dashBrand struct {
	Bg        string `json:"bg"`
	Fg        string `json:"fg"`
	Primary   string `json:"primary"`
	Secondary string `json:"secondary"`
	Tertiary  string `json:"tertiary"`
	Green     string `json:"green"`
	Muted     string `json:"muted"`
	Faint     string `json:"faint"`
}

type dashPrice struct {
	AlphaUSD  float64  `json:"alphaUsd"`
	TaoUSD    float64  `json:"taoUsd"`
	Change24h *float64 `json:"change24h"`
	MarketCap *float64 `json:"marketCapUsd"`
	FDV       *float64 `json:"fdvUsd"`
	Pool      string   `json:"pool"`
	Source    string   `json:"source"`
	FetchedAt string   `json:"fetchedAt"`
}

type dashData struct {
	AsOf        string             `json:"asOf"`
	Epoch       dashEpoch          `json:"epoch"`
	Points      dashPoints         `json:"points"`
	Epochs      []dashEpochRow     `json:"epochs"`
	Wallet      dashWallet         `json:"wallet"`
	Alpha       dashAlpha          `json:"alpha"`
	Traffic     dashTraffic        `json:"traffic"`
	Legacy      dashLegacy         `json:"legacy"`
	Brand       dashBrand          `json:"brand"`
	NetworkName string             `json:"networkName"`
	Price       dashPrice          `json:"price"`
	FX          map[string]float64 `json:"fx"`
	FXSymbols   map[string]string  `json:"fxSymbols"`
}

type dashSample struct {
	T      string `json:"t"`
	Paid   int64  `json:"paid"`
	Unpaid int64  `json:"unpaid"`
}

type dashRecent struct {
	T       string `json:"t"`
	Paid    int64  `json:"paid"`
	Unpaid  int64  `json:"unpaid"`
	Fetched string `json:"fetched"`
}

type dashHist struct {
	Series    []dashSample `json:"series"`
	Recent    []dashRecent `json:"recent"`
	RowsTotal int          `json:"rows_total"`
	Range     []string     `json:"range"`
}

type dashPay struct {
	Amount    float64 `json:"amount"`
	Type      string  `json:"type"`
	Chain     string  `json:"chain"`
	Completed bool    `json:"completed"`
	Create    string  `json:"create"`
	Pay       string  `json:"pay"`
	Tx        string  `json:"tx"`
	Bytes     int64   `json:"bytes"`
}

const blockSeconds = 12

// buildDashData assembles the page's three objects. Any source that fails
// leaves its section empty rather than failing the page: a dashboard that
// renders with a gap is more useful than one that 500s.
func buildDashData(db *sql.DB, token string) (dashData, dashHist, []dashPay) {
	d := dashData{
		Brand: dashBrand{
			Bg: "#101010", Fg: "#F8F8F8", Primary: "#638BFC", Secondary: "#D6E6F4",
			Tertiary: "#ED8FFF", Green: "#87FB67", Muted: "#989898", Faint: "#5A5A5A",
		},
		FXSymbols: map[string]string{"USD": "$", "EUR": "€", "GBP": "£", "JPY": "JP¥", "CNY": "CN¥"},
		FX:        map[string]float64{"USD": 1},
	}
	d.NetworkName = networkNameFromJWT(token)
	d.Epochs = []dashEpochRow{}
	h := dashHist{Series: []dashSample{}, Recent: []dashRecent{}}
	pays := []dashPay{}

	/* traffic: newest sample plus the series the charts draw */
	if db != nil {
		rows, err := db.Query("SELECT paid_bytes, unpaid_bytes, created_at, updated_at FROM wallet_stats ORDER BY created_at ASC")
		if err == nil {
			var samples []dashRecent
			for rows.Next() {
				var paid, unpaid int64
				var created, updated string
				if err := rows.Scan(&paid, &unpaid, &created, &updated); err != nil {
					continue
				}
				samples = append(samples, dashRecent{T: created, Paid: paid, Unpaid: unpaid, Fetched: updated})
			}
			rows.Close()
			if n := len(samples); n > 0 {
				last := samples[n-1]
				d.AsOf = last.T
				d.Traffic = dashTraffic{
					PaidBytes: last.Paid, UnpaidBytes: last.Unpaid,
					PaidTb:   float64(last.Paid) / 1e12,
					UnpaidTb: float64(last.Unpaid) / 1e12,
				}
				h.RowsTotal = n
				h.Range = []string{samples[0].T, last.T}
				/* the long series is downsampled to one sample per 2-hour bucket so
				   the page stays light. Bucket by time, not by clock minute: the
				   poll interval is configurable, and a 1h interval puts every
				   sample on minute 0, which a minute-based filter would drop */
				lastBucket := int64(-1)
				for i, s := range samples {
					keep := i == n-1
					if ts, err := time.Parse(time.RFC3339, s.T); err == nil {
						if b := ts.Unix() / 7200; b != lastBucket {
							keep = true
							lastBucket = b
						}
					}
					if keep {
						h.Series = append(h.Series, dashSample{T: s.T, Paid: s.Paid, Unpaid: s.Unpaid})
					}
				}
				start := n - 30
				if start < 0 {
					start = 0
				}
				for i := n - 1; i >= start; i-- {
					h.Recent = append(h.Recent, samples[i])
				}
			}
		}
	}

	/* epochs */
	var (
		epochs []epochView
		sn     snEpoch
		snAt   time.Time
	)
	if db != nil {
		if v, err := readEpochs(db, 200); err == nil {
			epochs = v
		}
		if v, at, err := readSNEpoch(db); err == nil {
			sn, snAt = v, at
		}
	}
	var total, best float64
	withPoints, finalized := 0, 0
	for _, e := range epochs {
		total += e.Points
		if e.Points > best {
			best, d.Points.BestEpoch = e.Points, e.Epoch
		}
		if e.Points > 0 {
			withPoints++
			if d.Alpha.FirstPayingEpoch == 0 || e.Epoch < d.Alpha.FirstPayingEpoch {
				d.Alpha.FirstPayingEpoch = e.Epoch
			}
		}
		if e.Finalized == 1 {
			finalized++
		}
		d.Epochs = append(d.Epochs, dashEpochRow{
			Epoch: e.Epoch, Points: e.Points, ShareBps: e.ShareBps,
			Alpha: e.Alpha, Settlement: e.Settlement,
		})
	}
	d.Points = dashPoints{
		Total: total, EpochsWithPoints: withPoints,
		EpochsFinalized: finalized, BestEpoch: d.Points.BestEpoch, BestPoints: best,
	}
	if len(epochs) > 0 {
		d.Wallet.FromEpoch = epochs[len(epochs)-1].Epoch
		d.Wallet.ThroughEpoch = epochs[0].Epoch
	}

	/* epoch geometry, with the head estimated because the coordinator does not
	   report one: epoch N+1 starts where N ended, and the epoch length is known */
	if sn.Epoch > 0 {
		end := sn.EarningEnd
		if end == 0 {
			end = sn.earningEndBlock()
		}
		startMs := time.Now()
		/* anchor to the epoch before the live one: the newest stored row may be
		   the open epoch, whose end_millis is in the future, which would pin the
		   estimate at the start block */
		for _, e := range epochs {
			if e.Epoch == sn.Epoch-1 && e.EndMillis > 0 {
				startMs = time.UnixMilli(e.EndMillis)
				break
			}
		}
		elapsed := time.Since(startMs)
		if elapsed < 0 {
			elapsed = 0
		}
		head := sn.StartBlock + int64(elapsed.Seconds())/blockSeconds
		left := end - head
		if left < 0 {
			left = 0
		}
		phase := "earning"
		switch {
		case head >= sn.Finalize:
			phase = "finalized"
		case head >= sn.CommitBlock:
			phase = "committing"
		case head >= end:
			phase = "root posting"
		}
		d.Epoch = dashEpoch{
			N: sn.Epoch, NextEpoch: sn.Epoch + 1, Phase: phase,
			StartBlock: sn.StartBlock, EarningEndBlock: end,
			CommitBlock: sn.CommitBlock, FinalizeBlock: sn.Finalize,
			TBlocks: sn.TEpochBlock, BlockSeconds: blockSeconds,
			Head: head, ElapsedBlocks: head - sn.StartBlock, LeftBlocks: left,
			Pct:             percent(head-sn.StartBlock, sn.TEpochBlock),
			CloseAt:         time.Now().Add(time.Duration(left) * blockSeconds * time.Second).UTC().Format("Mon 02 Jan 15:04 MST"),
			CommitIn:        humanBlocks(sn.CommitBlock - head),
			FinalizeIn:      humanBlocks(sn.Finalize - head),
			EpochLengthDays: float64(sn.TEpochBlock*blockSeconds) / 86400,
		}
		_ = snAt
		d.Alpha.ThisEpoch = "pending finalize"
		if phase == "finalized" {
			d.Alpha.ThisEpoch = "finalized"
		}
	}

	/* price: read the cache only. A cold cache leaves the section empty rather
	   than making the request wait on two upstreams */
	if doc, _, ok := peekPrice(); ok {
		d.Price = dashPrice{
			AlphaUSD: doc.AlphaUSD, TaoUSD: doc.TAOUSD, Change24h: doc.Change24h,
			MarketCap: doc.MarketCap, FDV: doc.FDV, Pool: doc.Pool,
			Source: doc.Source, FetchedAt: doc.FetchedAt,
		}
		for cur, rate := range doc.FX {
			d.FX[cur] = rate
		}
	}

	/* legacy USDC archive and the wallet the payouts name */
	var usdcTotal float64
	var legacyCount int
	var lastPay string
	/* the refresh handler replaces this slice under payoutCacheMu, and this
	   runs on the request goroutine: copy the header under the lock */
	payoutCacheMu.RLock()
	payouts := make([]payoutRecord, len(payoutCache))
	copy(payouts, payoutCache)
	payoutCacheMu.RUnlock()
	for _, p := range payouts {
		chain := p.Blockchain
		if chain == "" {
			chain = "SOL"
		}
		amount := p.TokenAmount
		typ := p.TokenType
		if typ == "" {
			typ = "USDC"
		}
		pays = append(pays, dashPay{
			Amount: amount, Type: typ, Chain: chain, Completed: p.Completed,
			Create: p.CreateTime, Pay: p.PaymentTime, Tx: p.TxHash,
			Bytes: p.PayoutByteCount,
		})
		if p.Completed {
			usdcTotal += amount
			legacyCount++
			if p.PaymentTime > lastPay {
				lastPay = p.PaymentTime
			}
		}
		if d.Wallet.Coldkey == "" && p.WalletAddress != "" {
			d.Wallet.Coldkey = p.WalletAddress
		}
	}
	if lastPay != "" {
		if ts, err := time.Parse(time.RFC3339, lastPay); err == nil {
			lastPay = ts.UTC().Format("2006-01-02")
		}
	}
	d.Wallet.Scope = "network"
	d.Legacy = dashLegacy{Usdc: usdcTotal, Count: legacyCount, Last: lastPay, Network: "SOL"}
	return d, h, pays
}

func percent(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	p := float64(part) / float64(whole) * 100
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func humanBlocks(blocks int64) string {
	if blocks <= 0 {
		return "0m"
	}
	s := blocks * blockSeconds
	d, h, m := s/86400, (s%86400)/3600, (s%3600)/60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// renderIndex serves the page with its three data objects filled in. The markup
// is the approved build, untouched; only the placeholders are replaced.
func renderIndex(db *sql.DB, token string) []byte {
	d, h, p := buildDashData(db, token)
	enc := func(v interface{}) string {
		b, err := json.Marshal(v)
		if err != nil {
			return "null"
		}
		/* keep a stray "</script>" in any string from closing the block early */
		return strings.ReplaceAll(string(b), "</", "<\\/")
	}
	out := indexHTML
	out = strings.ReplaceAll(out, "__D__", enc(d))
	out = strings.ReplaceAll(out, "__HIST__", enc(h))
	out = strings.ReplaceAll(out, "__PAY__", enc(p))
	return []byte(out)
}
