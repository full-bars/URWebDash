package main

import (
	"strings"
	"testing"
)

func TestIndexHTML_PhoneWebhookGrid(t *testing.T) {
	phone := sliceBetween(t, indexHTML, "@media(max-width:600px){", "\n}")
	if !strings.Contains(phone, ".dc-row{grid-template-columns:minmax(0,1fr)}") {
		t.Error("phone media block must let the webhook grid shrink")
	}
}
