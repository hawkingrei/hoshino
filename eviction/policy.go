package eviction

import (
	"fmt"
	"math"
	"time"
)

// EvictionPolicy defines the disk-pressure hysteresis used by Notify.
type EvictionPolicy struct {
	DiskCheckInterval           time.Duration
	MinPercentBlocksFree        float64
	EvictUntilPercentBlocksFree float64
}

func (p EvictionPolicy) Validate() error {
	if p.DiskCheckInterval <= 0 {
		return fmt.Errorf("disk check interval must be positive: %s", p.DiskCheckInterval)
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
	return nil
}

func validatePercent(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
		return fmt.Errorf("%s must be a finite percentage between 0 and 100: %g", name, value)
	}
	return nil
}

func (p EvictionPolicy) shouldStartEviction(percentBlocksFree float64) bool {
	return percentBlocksFree <= p.MinPercentBlocksFree
}

func (p EvictionPolicy) shouldContinueEviction(percentBlocksFree float64) bool {
	return percentBlocksFree < p.EvictUntilPercentBlocksFree
}

func (p EvictionPolicy) nextEvicting(current bool, percentBlocksFree float64) bool {
	if p.shouldStartEviction(percentBlocksFree) {
		return true
	}
	if !p.shouldContinueEviction(percentBlocksFree) {
		return false
	}
	return current
}
