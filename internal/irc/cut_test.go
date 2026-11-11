package irc

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLongTextsAreCutWithoutSplittingAnUmlaut(t *testing.T) {
	long := strings.Repeat("ä", 400) // 800 bytes
	got := cutBytes(long, 491)
	if !utf8.ValidString(got) || len(got) > 491 || len(got) < 489 {
		t.Fatalf("cut to %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if cutBytes("kurz", 10) != "kurz" {
		t.Fatal("short text changed")
	}
}
