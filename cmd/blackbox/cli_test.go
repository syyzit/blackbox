package main

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestParseAge(t *testing.T) {
	cases := map[string]time.Duration{
		"":    0,
		"30d": 30 * 24 * time.Hour,
		"2w":  14 * 24 * time.Hour,
		"12h": 12 * time.Hour,
		"90m": 90 * time.Minute,
	}
	for in, want := range cases {
		got, err := parseAge(in)
		if err != nil || got != want {
			t.Fatalf("parseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseAge("soon"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"":      0,
		"512":   512,
		"1KB":   1024,
		"2MB":   2 << 20,
		"1.5GB": 3 << 29,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Fatalf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseSize("lots"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestHumanBytes(t *testing.T) {
	if got := humanBytes(900); got != "900 B" {
		t.Fatalf("humanBytes(900) = %q", got)
	}
	if got := humanBytes(1536); got != "1.5 KB" {
		t.Fatalf("humanBytes(1536) = %q", got)
	}
}

func TestListenWithFallbackSkipsBusyPort(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port

	ln, url, err := listenWithFallback(fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("fallback failed: %v", err)
	}
	defer ln.Close()

	chosen := ln.Addr().(*net.TCPAddr).Port
	if chosen == port {
		t.Fatalf("reused the busy port %d", port)
	}
	if url != fmt.Sprintf("http://127.0.0.1:%d", chosen) {
		t.Fatalf("url = %q, chosen port %d", url, chosen)
	}
}
