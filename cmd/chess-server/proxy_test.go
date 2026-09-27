package main

import (
	"slices"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	got, err := parseTrustedProxies(" 10.0.0.1, 10.1.0.0/16,,::1 ")
	if err != nil || !slices.Equal(got, []string{"10.0.0.1", "10.1.0.0/16", "::1"}) {
		t.Fatalf("parse = %v, %v", got, err)
	}
	if got, err := parseTrustedProxies(""); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v, %v", got, err)
	}
	for _, invalid := range []string{"10.0.0", "10.0.0.0/33", "proxy.local"} {
		if _, err := parseTrustedProxies(invalid); err == nil {
			t.Errorf("%q accepted", invalid)
		}
	}
}
