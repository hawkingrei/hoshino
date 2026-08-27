package main

import (
	"flag"
	"testing"
)

func TestPprofHostDefaultsToLoopback(t *testing.T) {
	pprofHostFlag := flag.Lookup("pprof-host")
	if pprofHostFlag == nil {
		t.Fatal("pprof-host flag is not registered")
	}
	if got, want := pprofHostFlag.DefValue, "127.0.0.1"; got != want {
		t.Fatalf("pprof-host default = %q, want %q", got, want)
	}
}
