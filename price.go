package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Alpha price for subnet 25.
//
// The coordinator carries no price: /sn/price, /sn/stake, /sn/alpha and
// /sn/status all 404. The public subnet pool is quoted by GeckoTerminal on the
// "bittensor" network, where every subnet pool has the deterministic address
// "0-<netuid>" — the same keyless feed the ur.xyz site resolves against. The
// quote is alpha per TAO in USD, so one call answers both the alpha price and
// the TAO price.
//
// Currency conversion comes from CoinGecko's public simple-price endpoint,
// which returns TAO in each currency; the ratio to TAO-USD is the fx rate. That
// keeps the list short and honest: five currencies, no key, no rate-limit
// trouble at this cadence.

/* vars, not consts, so tests can point them at a local server */
var (
	geckoPoolURL = "https://api.geckoterminal.com/api/v2/networks/bittensor/pools/0-25"
	geckoFXURL   = "https://api.coingecko.com/api/v3/simple/price?ids=bittensor&vs_currencies=usd,eur,gbp,jpy,cny"
	priceTTL     = 60 * time.Second
)

type priceDoc struct {
	AlphaUSD    float64            `json:"alpha_usd"`
	TAOUSD      float64            `json:"tao_usd"`
	Change24h   float64            `json:"change_24h"`
	MarketCap   float64            `json:"market_cap_usd"`
	FDV         float64            `json:"fdv_usd"`
	Pool        string             `json:"pool"`
	Source      string             `json:"source"`
	FetchedAt   string             `json:"fetched_at"`
	Currencies  []string           `json:"currencies"`
	FX          map[string]float64 `json:"fx"`
	FetchedFrom string             `json:"-"`
}

// priceCache holds the last good document. A failed refresh keeps serving the
// previous quote rather than blanking the page; Stale reports how old it is.
type priceCacheState struct {
	mu   sync.Mutex
	doc  priceDoc
	at   time.Time
	fail string
}

var priceCache priceCacheState

type geckoPool struct {
	Data struct {
		Attributes struct {
			Name       string `json:"name"`
			BasePrice  string `json:"base_token_price_usd"`
			QuotePrice string `json:"quote_token_price_usd"`
			FDV        string `json:"fdv_usd"`
			MarketCap  string `json:"market_cap_usd"`
			Change     struct {
				H24 string `json:"h24"`
			} `json:"price_change_percentage"`
		} `json:"attributes"`
	} `json:"data"`
}

type geckoFX struct {
	Bittensor map[string]float64 `json:"bittensor"`
}

func getJSON(rawURL string, out interface{}, timeout time.Duration) error {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// parseFloat tolerates the empty and "null" strings these feeds use for a
// value they do not have, so one missing field cannot fail the whole quote.
func parseFloat(s string) float64 {
	if s == "" || s == "null" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// fetchPrice reads the pool quote and the fx quotes. The pool call is required;
// fx is optional, because a price without conversion is still useful and the fx
// endpoint is the one more likely to be rate limited.
func fetchPrice() (priceDoc, error) {
	var pool geckoPool
	if err := getJSON(geckoPoolURL, &pool, 15*time.Second); err != nil {
		return priceDoc{}, fmt.Errorf("pool quote: %w", err)
	}
	a := pool.Data.Attributes
	doc := priceDoc{
		AlphaUSD:   parseFloat(a.BasePrice),
		TAOUSD:     parseFloat(a.QuotePrice),
		Change24h:  parseFloat(a.Change.H24),
		MarketCap:  parseFloat(a.MarketCap),
		FDV:        parseFloat(a.FDV),
		Pool:       a.Name,
		Source:     "geckoterminal pool 0-25",
		FetchedAt:  time.Now().UTC().Format(time.RFC3339),
		FX:         map[string]float64{"USD": 1},
		Currencies: []string{"USD"},
	}
	if doc.AlphaUSD <= 0 {
		return priceDoc{}, fmt.Errorf("pool quote: alpha price missing")
	}
	var fx geckoFX
	if err := getJSON(geckoFXURL, &fx, 15*time.Second); err == nil {
		if usd, ok := fx.Bittensor["usd"]; ok && usd > 0 {
			for _, cur := range []string{"eur", "gbp", "jpy", "cny"} {
				if v, ok := fx.Bittensor[cur]; ok && v > 0 {
					doc.FX[strings.ToUpper(cur)] = v / usd
				}
			}
		}
	}
	for _, cur := range []string{"USD", "EUR", "GBP", "JPY", "CNY"} {
		if _, ok := doc.FX[cur]; ok && cur != "USD" {
			doc.Currencies = append(doc.Currencies, cur)
		}
	}
	return doc, nil
}

// cachedPrice returns the document, refreshing it when older than the TTL.
// Concurrent refreshes collapse onto one.
func cachedPrice() (priceDoc, time.Duration, error) {
	priceCache.mu.Lock()
	defer priceCache.mu.Unlock()
	age := time.Since(priceCache.at)
	if priceCache.doc.AlphaUSD > 0 && age < priceTTL {
		return priceCache.doc, age, nil
	}
	doc, err := fetchPrice()
	if err != nil {
		/* keep serving the last good quote; the age tells the page it is old */
		if priceCache.doc.AlphaUSD > 0 {
			priceCache.fail = err.Error()
			return priceCache.doc, age, nil
		}
		return priceDoc{}, 0, err
	}
	priceCache.doc, priceCache.at, priceCache.fail = doc, time.Now(), ""
	return doc, 0, nil
}

func handlePrice(w http.ResponseWriter, r *http.Request) {
	doc, age, err := cachedPrice()
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	doc.FetchedFrom = ""
	payload := map[string]interface{}{
		"price":     doc,
		"cached_s":  int(age.Seconds()),
		"refreshed": age < priceTTL,
	}
	json.NewEncoder(w).Encode(payload)
}
