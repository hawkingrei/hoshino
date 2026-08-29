package main

import (
	"testing"
)

func TestParseOptionsUsesSafePilotDefaults(t *testing.T) {
	opts, err := parseOptions([]string{"--cache-dir=/var/cache/bazel"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.shadow {
		t.Fatal("shadow mode must be enabled by default")
	}
	if opts.pprofPort != 0 {
		t.Fatalf("pprof port = %d, want disabled", opts.pprofPort)
	}
	if opts.maxCacheGiB != 400 || opts.targetCacheGiB != 320 {
		t.Fatalf("cache bounds = %d/%d GiB, want 400/320", opts.maxCacheGiB, opts.targetCacheGiB)
	}
	if opts.activityLock != "/var/cache/bazel-cache.activity.lock" {
		t.Fatalf("activity lock = %q", opts.activityLock)
	}
}

func TestEvictionConfigRejectsInvalidBounds(t *testing.T) {
	opts, err := parseOptions([]string{
		"--cache-dir=/var/cache/bazel",
		"--max-cache-gib=100",
		"--target-cache-gib=100",
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := opts.evictionConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Policy.Validate(); err == nil {
		t.Fatal("invalid cache bounds were accepted")
	}
}
