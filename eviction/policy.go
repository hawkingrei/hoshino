package eviction

import (
	"fmt"
	"math"
	"time"
)

type EvictionPolicy struct {
	DiskCheckInterval           time.Duration
	CheckpointInterval          time.Duration
	TopKDecayInterval           time.Duration
	MinimumResidence            time.Duration
	WarmupDuration              time.Duration
	MinPercentBlocksFree        float64
	EvictUntilPercentBlocksFree float64
	MaxCacheBytes               int64
	TargetCacheBytes            int64
	MinActionCacheBytes         int64
	CleanupBatchSize            int
	TopKCapacity                uint32
	TopKWidth                   uint32
	TopKDepth                   uint32
	TopKDecay                   float64
	TopKMinCount                uint32
	Shadow                      bool
}

type pressureSnapshot struct {
	percentBlocksFree float64
	cacheBytes        int64
}

func (p EvictionPolicy) Validate() error {
	if p.DiskCheckInterval <= 0 {
		return fmt.Errorf("disk check interval must be positive: %s", p.DiskCheckInterval)
	}
	if p.CheckpointInterval <= 0 {
		return fmt.Errorf("checkpoint interval must be positive: %s", p.CheckpointInterval)
	}
	if p.TopKDecayInterval <= 0 {
		return fmt.Errorf("top-k decay interval must be positive: %s", p.TopKDecayInterval)
	}
	if p.MinimumResidence < 0 || p.WarmupDuration < 0 {
		return fmt.Errorf("minimum residence and warmup durations must not be negative")
	}
	if err := validatePercent("minimum free blocks", p.MinPercentBlocksFree); err != nil {
		return err
	}
	if err := validatePercent("eviction target", p.EvictUntilPercentBlocksFree); err != nil {
		return err
	}
	if p.MinPercentBlocksFree >= p.EvictUntilPercentBlocksFree {
		return fmt.Errorf(
			"minimum free blocks must be lower than eviction target: minimum=%g target=%g",
			p.MinPercentBlocksFree,
			p.EvictUntilPercentBlocksFree,
		)
	}
	if p.MaxCacheBytes <= 0 || p.TargetCacheBytes < 0 || p.TargetCacheBytes >= p.MaxCacheBytes {
		return fmt.Errorf("cache target must be non-negative and lower than the positive maximum: target=%d maximum=%d", p.TargetCacheBytes, p.MaxCacheBytes)
	}
	if p.MinActionCacheBytes < 0 || p.MinActionCacheBytes > p.TargetCacheBytes {
		return fmt.Errorf("minimum action-cache bytes must be between zero and the cache target: %d", p.MinActionCacheBytes)
	}
	if p.CleanupBatchSize <= 0 {
		return fmt.Errorf("cleanup batch size must be positive: %d", p.CleanupBatchSize)
	}
	if p.TopKCapacity == 0 || p.TopKWidth == 0 || p.TopKDepth == 0 || p.TopKMinCount == 0 {
		return fmt.Errorf("top-k capacity, width, depth, and minimum count must be positive")
	}
	if math.IsNaN(p.TopKDecay) || math.IsInf(p.TopKDecay, 0) || p.TopKDecay <= 0 || p.TopKDecay >= 1 {
		return fmt.Errorf("top-k decay must be between zero and one: %g", p.TopKDecay)
	}
	return nil
}

func validatePercent(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
		return fmt.Errorf("%s must be a finite percentage between 0 and 100: %g", name, value)
	}
	return nil
}

func (p EvictionPolicy) nextEvicting(current bool, snapshot pressureSnapshot) bool {
	if !current {
		return snapshot.percentBlocksFree <= p.MinPercentBlocksFree || snapshot.cacheBytes >= p.MaxCacheBytes
	}
	return snapshot.percentBlocksFree < p.EvictUntilPercentBlocksFree || snapshot.cacheBytes > p.TargetCacheBytes
}
