package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestParseFloat(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"0.984516883663126", 0.984516883663126, true},
		{"233.5", 233.5, true},
		{"", 0, false},
		{"null", 0, false},
		{"not a number", 0, false},
	}
	for _, c := range cases {
		got, ok := parseFloat(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseFloat(%q)=(%v,%v) want (%v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// stubFeeds points the two feed urls at one local server. The pool always
// answers; the fx endpoint answers unless failFX is set.
func stubFeeds(t *testing.T, failFX bool) *int {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/fx" {
			if failFX {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Write([]byte(`{"bittensor":{"usd":233.5,"eur":209.0,"gbp":174.0,"jpy":36900,"cny":1550}}`))
			return
		}
		w.Write([]byte(`{"data":{"attributes":{"name":"SN25 / TAO","base_token_price_usd":"0.98",
			"quote_token_price_usd":"233.5","fdv_usd":"5200000","market_cap_usd":"5190000",
			"price_change_percentage":{"h24":"-2.5"}}}}`))
	}))
	t.Cleanup(srv.Close)
	oldPool, oldFX, oldTTL := geckoPoolURL, geckoFXURL, priceTTL
	geckoPoolURL, geckoFXURL = srv.URL+"/pool", srv.URL+"/fx"
	priceTTL = 60 * time.Second
	priceCache = priceCacheState{}
	t.Cleanup(func() {
		geckoPoolURL, geckoFXURL, priceTTL = oldPool, oldFX, oldTTL
		priceCache = priceCacheState{}
	})
	return &hits
}

func TestFetchPriceParsesPoolAndFX(t *testing.T) {
	stubFeeds(t, false)
	doc, err := fetchPrice()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if doc.AlphaUSD != 0.98 || doc.TAOUSD != 233.5 {
		t.Errorf("prices: %+v", doc)
	}
	if doc.Change24h == nil || *doc.Change24h != -2.5 {
		t.Errorf("change: %v", doc.Change24h)
	}
	if doc.MarketCap == nil || *doc.MarketCap != 5190000 {
		t.Errorf("mcap: %v", doc.MarketCap)
	}
	if got := doc.FX["EUR"]; got < 0.89 || got > 0.90 {
		t.Errorf("eur rate: %v", got)
	}
	if len(doc.Currencies) != 5 {
		t.Errorf("currencies: %v", doc.Currencies)
	}
}

// A missing fx quote must not cost the alpha price: conversion is the optional
// half of the document.
func TestFetchPriceSurvivesMissingFX(t *testing.T) {
	stubFeeds(t, true)
	doc, err := fetchPrice()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if doc.AlphaUSD == 0 {
		t.Fatal("alpha price lost")
	}
	if len(doc.Currencies) != 1 || doc.Currencies[0] != "USD" {
		t.Errorf("want USD only, got %v", doc.Currencies)
	}
}

func TestCachedPriceUsesTTL(t *testing.T) {
	hits := stubFeeds(t, false)
	if _, _, err := cachedPrice(); err != nil {
		t.Fatalf("first: %v", err)
	}
	after := *hits
	if _, _, err := cachedPrice(); err != nil {
		t.Fatalf("second: %v", err)
	}
	if *hits != after {
		t.Errorf("second call hit the network: %d -> %d", after, *hits)
	}
	// expiring the entry forces a refetch
	priceCache.at = time.Now().Add(-2 * priceTTL)
	if _, _, err := cachedPrice(); err != nil {
		t.Fatalf("third: %v", err)
	}
	if *hits == after {
		t.Error("expired entry was not refetched")
	}
}

func TestHandlePriceWithNoQuoteReturns502(t *testing.T) {
	oldPool := geckoPoolURL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	geckoPoolURL = srv.URL + "/pool"
	priceCache = priceCacheState{}
	t.Cleanup(func() { geckoPoolURL = oldPool; priceCache = priceCacheState{} })

	rec := httptest.NewRecorder()
	handlePrice(rec, httptest.NewRequest("GET", "/api/price", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("want 502, got %d", rec.Code)
	}
}

func TestHandlePriceShape(t *testing.T) {
	stubFeeds(t, false)
	rec := httptest.NewRecorder()
	handlePrice(rec, httptest.NewRequest("GET", "/api/price", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Price struct {
			AlphaUSD float64            `json:"alpha_usd"`
			FX       map[string]float64 `json:"fx"`
		} `json:"price"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if body.Price.AlphaUSD == 0 || body.Price.FX["USD"] != 1 {
		t.Errorf("shape: %+v", body.Price)
	}
}

// An fx miss on a later refresh must keep the conversions learned earlier.
func TestRefreshKeepsEarlierFX(t *testing.T) {
	stubFeeds(t, false)
	if _, _, err := cachedPrice(); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, ok := priceCache.doc.FX["EUR"]; !ok {
		t.Fatal("eur rate not stored")
	}
	// same pool feed, fx now failing
	oldFX := geckoFXURL
	geckoFXURL = "http://127.0.0.1:1/fx"
	t.Cleanup(func() { geckoFXURL = oldFX })
	priceCache.at = time.Now().Add(-2 * priceTTL)
	if _, _, err := cachedPrice(); err != nil {
		t.Fatalf("second: %v", err)
	}
	if _, ok := priceCache.doc.FX["EUR"]; !ok {
		t.Error("earlier eur rate was wiped by a failed fx fetch")
	}
}

// A failed refresh must not be retried on every request. The stub itself
// returns 500 for the pool, so the hit counter is the evidence.
func TestFailureBacksOff(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	oldPool, oldFX := geckoPoolURL, geckoFXURL
	geckoPoolURL, geckoFXURL = srv.URL+"/pool", srv.URL+"/fx"
	priceCache = priceCacheState{}
	t.Cleanup(func() {
		geckoPoolURL, geckoFXURL = oldPool, oldFX
		priceCache = priceCacheState{}
	})

	if _, _, err := cachedPrice(); err == nil {
		t.Fatal("expected the first failure to surface")
	}
	mu.Lock()
	after := hits
	mu.Unlock()
	if _, _, err := cachedPrice(); err == nil {
		t.Fatal("expected an error while backing off")
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != after {
		t.Errorf("backoff ignored: %d -> %d upstream calls", after, hits)
	}
}

// A failed refresh must not make an old quote look new: the age returned is
// measured from the last GOOD quote, not from the failed attempt.
func TestFailedRefreshKeepsQuoteAge(t *testing.T) {
	hits := stubFeeds(t, false)
	_ = hits
	if _, _, err := cachedPrice(); err != nil {
		t.Fatalf("first: %v", err)
	}
	/* age the good quote past the ttl, then make the pool fail */
	priceCache.mu.Lock()
	priceCache.at = time.Now().Add(-10 * time.Minute)
	priceCache.mu.Unlock()
	oldPool := geckoPoolURL
	geckoPoolURL = "http://127.0.0.1:1/pool"
	t.Cleanup(func() { geckoPoolURL = oldPool })

	doc, age, err := cachedPrice()
	if err != nil {
		t.Fatalf("want the last good quote, got %v", err)
	}
	if doc.AlphaUSD == 0 {
		t.Fatal("quote lost")
	}
	if age < 9*time.Minute {
		t.Errorf("a failed refresh reported the quote as %v old; it is ~10m", age)
	}
}

// A reader arriving while a refresh is in flight must not be made to wait for
// it: it gets the previous quote and its age.
func TestReaderDoesNotBlockBehindRefresh(t *testing.T) {
	stubFeeds(t, false)
	if _, _, err := cachedPrice(); err != nil {
		t.Fatalf("prime: %v", err)
	}
	priceCache.mu.Lock()
	priceCache.at = time.Now().Add(-5 * time.Minute) // stale, so the next call refreshes
	priceCache.refreshing = true                     // as if another request were fetching
	priceCache.mu.Unlock()

	start := time.Now()
	doc, age, err := cachedPrice()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("want the cached quote, got %v", err)
	}
	if doc.AlphaUSD == 0 {
		t.Fatal("quote lost")
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("reader waited %v behind an in-flight refresh", elapsed)
	}
	if age < 4*time.Minute {
		t.Errorf("age should reflect the stale quote, got %v", age)
	}
}

// A missing 24h change must serialise as null, not as a confident zero.
func TestMissingChangeSerialisesAsNull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fx" {
			w.Write([]byte(`{"bittensor":{"usd":233.5}}`))
			return
		}
		w.Write([]byte(`{"data":{"attributes":{"name":"SN25 / TAO","base_token_price_usd":"0.98",
			"quote_token_price_usd":"233.5","market_cap_usd":"","price_change_percentage":{}}}}`))
	}))
	t.Cleanup(srv.Close)
	oldPool, oldFX := geckoPoolURL, geckoFXURL
	geckoPoolURL, geckoFXURL = srv.URL+"/pool", srv.URL+"/fx"
	priceCache = priceCacheState{}
	t.Cleanup(func() {
		geckoPoolURL, geckoFXURL = oldPool, oldFX
		priceCache = priceCacheState{}
	})

	rec := httptest.NewRecorder()
	handlePrice(rec, httptest.NewRequest("GET", "/api/price", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Price map[string]json.RawMessage `json:"price"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	if got := string(body.Price["change_24h"]); got != "null" {
		t.Errorf("change_24h serialised as %s, want null", got)
	}
	if got := string(body.Price["market_cap_usd"]); got != "null" {
		t.Errorf("market_cap_usd serialised as %s, want null", got)
	}
}
