package eviction

import (
	"math"
	"testing"
	"time"
)

func TestEvictionPolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		policy  EvictionPolicy
		wantErr bool
	}{
		{
			name: "valid",
			policy: EvictionPolicy{
				DiskCheckInterval:           10 * time.Second,
				MinPercentBlocksFree:        5,
				EvictUntilPercentBlocksFree: 20,
			},
		},
		{
			name: "non-positive interval",
			policy: EvictionPolicy{
				MinPercentBlocksFree:        5,
				EvictUntilPercentBlocksFree: 20,
			},
			wantErr: true,
		},
		{
			name: "invalid minimum",
			policy: EvictionPolicy{
				DiskCheckInterval:           10 * time.Second,
				MinPercentBlocksFree:        math.NaN(),
				EvictUntilPercentBlocksFree: 20,
			},
			wantErr: true,
		},
		{
			name: "invalid target",
			policy: EvictionPolicy{
				DiskCheckInterval:           10 * time.Second,
				MinPercentBlocksFree:        5,
				EvictUntilPercentBlocksFree: 101,
			},
			wantErr: true,
		},
		{
			name: "missing hysteresis",
			policy: EvictionPolicy{
				DiskCheckInterval:           10 * time.Second,
				MinPercentBlocksFree:        20,
				EvictUntilPercentBlocksFree: 20,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEvictionPolicyThresholds(t *testing.T) {
	policy := EvictionPolicy{
		DiskCheckInterval:           10 * time.Second,
		MinPercentBlocksFree:        5,
		EvictUntilPercentBlocksFree: 20,
	}

	if !policy.shouldStartEviction(5) {
		t.Fatal("eviction should start at the minimum free-space threshold")
	}
	if policy.shouldStartEviction(5.1) {
		t.Fatal("eviction should not start above the minimum free-space threshold")
	}
	if !policy.shouldContinueEviction(19.9) {
		t.Fatal("eviction should continue below the target free-space threshold")
	}
	if policy.shouldContinueEviction(20) {
		t.Fatal("eviction should stop at the target free-space threshold")
	}
	if policy.nextEvicting(false, 10) {
		t.Fatal("eviction should remain inactive inside the hysteresis band")
	}
	if !policy.nextEvicting(true, 10) {
		t.Fatal("eviction should remain active inside the hysteresis band")
	}
}
