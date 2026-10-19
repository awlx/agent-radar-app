package main

import (
	"testing"
)

func TestWithoutPSN(t *testing.T) {
	got := withoutPSN([]string{"-psn_0_12345", "-poll", "1s"})
	if len(got) != 2 || got[0] != "-poll" {
		t.Fatalf("got %q", got)
	}
}
