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

	/* serve cache only: fetching on a page load is what made the chart appear and
	   disappear, because the free API allows a handful of calls a minute and this
	   endpoint is hit on every visit. The warmer below keeps the ranges fresh. */
	priceHist.mu.Lock()
	pts, ok := priceHist.data[sel]
	priceHist.mu.Unlock()
	if !ok || len(pts) == 0 {
		/* a short window can be cut from a longer cached one instead of asking
		   the API again */
		pts = deriveRange(sel)
	}
	encodePoints(w, pts)
}

/*
deriveRange falls back to a coarser cached series: the last twelve hourly

	points are the last half day, which is close enough for a trend line while the
	real window is unavailable.
*/
func deriveRange(sel string) []pricePoint {
	priceHist.mu.Lock()
	defer priceHist.mu.Unlock()
	if sel != "1h" {
		return nil
	}
	src := priceHist.data["24h"]
	if len(src) < 2 {
		return nil
	}
	n := 12
	if len(src) < n {
		n = len(src)
	}
	out := make([]pricePoint, n)
	copy(out, src[len(src)-n:])
	return out
}

/*
priceHistoryWarmer fills every range in the background, one call at a time and

	spaced out, so the endpoint never has to touch the network on a request.
*/
func priceHistoryWarmer() {
	order := []string{"24h", "7d", "30d", "1h"}
	for {
		for _, sel := range order {
			priceHist.mu.Lock()
			fresh := time.Since(priceHist.at[sel]) < priceHistTTL(sel)
			parked := time.Now().Before(priceHist.blocked[sel])
			priceHist.mu.Unlock()
			if fresh || parked {
				continue
			}
			if pts, err := fetchOHLCV(ohlcvRanges[sel]); err == nil && len(pts) > 0 {
				priceHist.mu.Lock()
				priceHist.data[sel] = pts
				priceHist.at[sel] = time.Now()
				priceHist.mu.Unlock()
			} else if err != nil {
				priceHist.mu.Lock()
				priceHist.blocked[sel] = time.Now().Add(90 * time.Second)
				priceHist.mu.Unlock()
			}
			time.Sleep(12 * time.Second) /* stay under the API rate limit */
		}
		time.Sleep(time.Minute)
	}
}

/*
always an array: a nil slice marshals to null, which reads as a broken payload

	to the chart rather than "no samples yet".
*/
func encodePoints(w http.ResponseWriter, pts []pricePoint) {
	if pts == nil {
		pts = []pricePoint{}
	}
	_ = json.NewEncoder(w).Encode(pts)
}
