package eviction

import (
	"math"
	"testing"
	"time"
)

func validPolicy() EvictionPolicy {
	return EvictionPolicy{
		DiskCheckInterval:           10 * time.Second,
		CheckpointInterval:          5 * time.Minute,
		TopKDecayInterval:           time.Hour,
		MinimumResidence:            time.Hour,
		WarmupDuration:              30 * time.Minute,
		MinPercentBlocksFree:        30,
		EvictUntilPercentBlocksFree: 35,
		MaxCacheBytes:               400 << 30,
		TargetCacheBytes:            320 << 30,
		MinActionCacheBytes:         4 << 30,
		CleanupBatchSize:            100,
		TopKCapacity:                100_000,
		TopKWidth:                   1 << 17,
		TopKDepth:                   4,
		TopKDecay:                   0.9,
		TopKMinCount:                1,
	}
}

func TestEvictionPolicyValidate(t *testing.T) {
	for _, mutate := range []func(*EvictionPolicy){
		func(p *EvictionPolicy) { p.DiskCheckInterval = 0 },
		func(p *EvictionPolicy) { p.CheckpointInterval = 0 },
		func(p *EvictionPolicy) { p.TopKDecayInterval = 0 },
		func(p *EvictionPolicy) { p.MinPercentBlocksFree = math.NaN() },
		func(p *EvictionPolicy) { p.EvictUntilPercentBlocksFree = 101 },
		func(p *EvictionPolicy) { p.MinPercentBlocksFree = p.EvictUntilPercentBlocksFree },
		func(p *EvictionPolicy) { p.TargetCacheBytes = p.MaxCacheBytes },
		func(p *EvictionPolicy) { p.MinActionCacheBytes = p.TargetCacheBytes + 1 },
		func(p *EvictionPolicy) { p.TopKWidth = 0 },
		func(p *EvictionPolicy) { p.TopKDecay = 1 },
	} {
		policy := validPolicy()
		mutate(&policy)
		if err := policy.Validate(); err == nil {
			t.Fatalf("Validate() accepted invalid policy: %#v", policy)
		}
	}
	if err := validPolicy().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestEvictionPolicyCombinesCacheAndNodePressure(t *testing.T) {
	policy := validPolicy()
	if !policy.nextEvicting(false, pressureSnapshot{percentBlocksFree: 50, cacheBytes: policy.MaxCacheBytes}) {
		t.Fatal("cache high watermark did not start eviction")
	}
	if !policy.nextEvicting(false, pressureSnapshot{percentBlocksFree: policy.MinPercentBlocksFree, cacheBytes: 0}) {
		t.Fatal("node free-space floor did not start eviction")
	}
	if !policy.nextEvicting(true, pressureSnapshot{percentBlocksFree: 50, cacheBytes: policy.TargetCacheBytes + 1}) {
		t.Fatal("eviction stopped above the cache target")
	}
	if !policy.nextEvicting(true, pressureSnapshot{percentBlocksFree: policy.EvictUntilPercentBlocksFree - 0.1, cacheBytes: 0}) {
		t.Fatal("eviction stopped below the node free-space target")
	}
	if policy.nextEvicting(true, pressureSnapshot{percentBlocksFree: policy.EvictUntilPercentBlocksFree, cacheBytes: policy.TargetCacheBytes}) {
		t.Fatal("eviction did not stop after both targets were met")
	}
}
