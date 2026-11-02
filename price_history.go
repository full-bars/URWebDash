package main

// Alpha price history.
//
// A single quote tells you nothing about direction, so the dashboard draws a
// trend line. The price already comes from GeckoTerminal's pool quote for
// subnet 25, and the same API serves OHLCV candles, so the chart reads real
// history instead of starting empty and filling as this process samples.
//
// One cache per range: the API is rate limited, and a chart is not worth a
// request per page load.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type pricePoint struct {
	T     int64   `json:"t"`
	Close float64 `json:"close"`
}

type ohlcvSpec struct {
	timeframe string
	aggregate int
	limit     int
}

/*
minute candles cover the last hour, hourly candles the day and the week, daily

	candles the month. Limits are the number of candles, so 168 hours is 7 days.
*/
var ohlcvRanges = map[string]ohlcvSpec{
	"1h":  {"minute", 5, 12},
	"24h": {"hour", 1, 24},
	"7d":  {"hour", 1, 168},
	"30d": {"day", 1, 30},
}

/*
the free API allows a few requests a minute and answers 429 when pushed, so

	each range is cached on its own clock: the longer the window, the longer a
	sample stays useful. A 429 parks that range for a while instead of retrying
	on every page load.
*/
type priceHistCache struct {
	mu      sync.Mutex
	at      map[string]time.Time
	data    map[string][]pricePoint
	blocked map[string]time.Time
}

var priceHist = priceHistCache{at: map[string]time.Time{}, data: map[string][]pricePoint{}, blocked: map[string]time.Time{}}

func priceHistTTL(r string) time.Duration {
	switch r {
	case "1h":
		return 5 * time.Minute
	case "24h":
		return 10 * time.Minute
	case "7d":
		return 15 * time.Minute
	default:
		return 30 * time.Minute
	}
}

func fetchOHLCV(spec ohlcvSpec) ([]pricePoint, error) {
	url := "https://api.geckoterminal.com/api/v2/networks/bittensor/pools/0-25/ohlcv/" +
		spec.timeframe + "?aggregate=" + strconv.Itoa(spec.aggregate) +
		"&limit=" + strconv.Itoa(spec.limit) + "&currency=usd&token=base"
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ohlcv: HTTP %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var out struct {
		Data struct {
			Attributes struct {
				List [][]float64 `json:"ohlcv_list"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	/* rows arrive newest first: [ts, open, high, low, close, volume] */
	list := out.Data.Attributes.List
	pts := make([]pricePoint, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		row := list[i]
		if len(row) < 5 {
			continue
		}
		pts = append(pts, pricePoint{T: int64(row[0]), Close: row[4]})
	}
	return pts, nil
}

func handlePriceHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	sel := strings.TrimSpace(r.URL.Query().Get("range"))
	if _, ok := ohlcvRanges[sel]; !ok {
		sel = "24h"
	}

	priceHist.mu.Lock()
	if pts, ok := priceHist.data[sel]; ok && len(pts) > 0 && time.Since(priceHist.at[sel]) < priceHistTTL(sel) {
		priceHist.mu.Unlock()
		_ = json.NewEncoder(w).Encode(pts)
		return
	}
	if until, parked := priceHist.blocked[sel]; parked && time.Now().Before(until) {
		cached := priceHist.data[sel]
		priceHist.mu.Unlock()
		_ = json.NewEncoder(w).Encode(cached)
		return
	}
	priceHist.mu.Unlock()

	pts, err := fetchOHLCV(ohlcvRanges[sel])
	if err != nil {
		priceHist.mu.Lock()
		/* park the range: a rate limit must not turn into a retry storm */
		priceHist.blocked[sel] = time.Now().Add(5 * time.Minute)
		cached := priceHist.data[sel]
		priceHist.mu.Unlock()
		_ = json.NewEncoder(w).Encode(cached)
		return
	}

	priceHist.mu.Lock()
	priceHist.data[sel] = pts
	priceHist.at[sel] = time.Now()
	delete(priceHist.blocked, sel)
	priceHist.mu.Unlock()

	_ = json.NewEncoder(w).Encode(pts)
}
