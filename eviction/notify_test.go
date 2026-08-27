package eviction

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDiskUsageSnapshotRefresh(t *testing.T) {
	now := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	usage := diskUsageSnapshot{}
	calls := 0
	getDiskUsage := func(path string) (float64, uint64, uint64, error) {
		calls++
		require.Equal(t, "/cache", path)
		return float64(80 - calls), 0, 0, nil
	}

	require.NoError(t, usage.refresh(now, "/cache", getDiskUsage))
	require.Equal(t, 1, calls)
	require.Equal(t, float64(79), usage.blocksFree)

	require.NoError(t, usage.refresh(now.Add(diskUsageRefreshInterval-time.Nanosecond), "/cache", getDiskUsage))
	require.Equal(t, 1, calls)
	require.Equal(t, float64(79), usage.blocksFree)

	require.NoError(t, usage.refresh(now.Add(diskUsageRefreshInterval), "/cache", getDiskUsage))
	require.Equal(t, 2, calls)
	require.Equal(t, float64(78), usage.blocksFree)
}

func TestDiskUsageSnapshotRefreshRetriesAfterError(t *testing.T) {
	now := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	usage := diskUsageSnapshot{}
	expectedErr := errors.New("disk usage unavailable")
	calls := 0
	getDiskUsage := func(string) (float64, uint64, uint64, error) {
		calls++
		if calls == 1 {
			return 0, 0, 0, expectedErr
		}
		return 75, 0, 0, nil
	}

	require.ErrorIs(t, usage.refresh(now, "/cache", getDiskUsage), expectedErr)
	require.True(t, usage.checkedAt.IsZero())

	require.NoError(t, usage.refresh(now, "/cache", getDiskUsage))
	require.Equal(t, 2, calls)
	require.Equal(t, float64(75), usage.blocksFree)
	require.Equal(t, now, usage.checkedAt)
}
