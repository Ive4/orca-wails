package main

import (
	"strings"
	"testing"
)

func TestValidatedOfferKeepsPairingOnOwnedWebPage(t *testing.T) {
	const secret = "not-for-logs"
	valid := "http://127.0.0.1:26768/web-index.html#pairing=" + secret
	if _, err := validatedOffer(valid, 26768); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{
		strings.Replace(valid, "127.0.0.1", "[::1]", 1),
		strings.Replace(valid, "26768", "26769", 1),
		strings.Replace(valid, "web-index.html", "other.html", 1),
		strings.Replace(valid, "#", "?override=1#", 1),
	} {
		if _, err := validatedOffer(candidate, 26768); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("invalid offer accepted or pairing credential exposed")
		}
	}
}
