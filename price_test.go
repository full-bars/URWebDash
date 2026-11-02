package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseFloat(t *testing.T) {
	cases := map[string]float64{
		"0.984516883663126": 0.984516883663126,
		"233.5":             233.5,
		"":                  0,
		"null":              0,
		"not a number":      0,
	}
	for in, want := range cases {
		if got := parseFloat(in); got != want {
			t.Errorf("parseFloat(%q)=%v want %v", in, got, want)
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
	if doc.Change24h != -2.5 {
		t.Errorf("change: %v", doc.Change24h)
	}
	if doc.MarketCap != 5190000 {
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
