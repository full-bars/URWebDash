package main

// A recorded payload can stand in for the live coordinator call so the payout
// section can be exercised offline with real data. Test-only: nothing reads it
// unless PAYOUT_FIXTURE_FILE names a readable file.
//
// Two shapes are accepted, because a recorded fixture may come either from the
// coordinator (account_payments / account_points / error.message) or from this
// dashboard's own /api/payout-stats (payments with a flat error string).

import (
	"encoding/json"
	"os"
	"strings"
)

func payoutSource(token string) (*accountResponse, error) {
	f := strings.TrimSpace(os.Getenv("PAYOUT_FIXTURE_FILE"))
	if f == "" {
		return fetchPayouts(token)
	}
	raw, err := os.ReadFile(f)
	if err != nil {
		return nil, err
	}

	/* coordinator shape first */
	var direct accountResponse
	if err := json.Unmarshal(raw, &direct); err == nil && len(direct.AccountPayments) > 0 {
		return &direct, nil
	}

	/* dashboard shape: payments[], flat error string */
	var dash struct {
		Payments []payoutRecord `json:"payments"`
		Points   float64        `json:"points"`
		Error    string         `json:"error"`
	}
	if err := json.Unmarshal(raw, &dash); err != nil {
		return nil, err
	}
	if dash.Error != "" {
		return &accountResponse{Error: &struct {
			Message string `json:"message"`
		}{Message: dash.Error}}, nil
	}
	return &accountResponse{AccountPayments: dash.Payments}, nil
}
