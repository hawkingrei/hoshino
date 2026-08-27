package eviction

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hawkingrei/hoshino/diskutil"
	"github.com/hawkingrei/hoshino/eviction/internal/heavykeeper"
)

func TestTopkCleanerStopsAtEvictionTarget(t *testing.T) {
	cacheDir := t.TempDir()
	paths := createCacheFiles(t, cacheDir, 250)
	protected := paths[0]
	diskChecks := 0
	notify := &Notify{
		path:                        cacheDir,
		disk:                        diskutil.NewCache(cacheDir),
		heavykeeper:                 &cleanupTopk{items: []heavykeeper.Item{{Key: protected, Count: 1}}},
		evictUntilPercentBlocksFree: 20,
		getCleanupDiskUsage: func(string) (float64, uint64, uint64, error) {
			diskChecks++
			switch diskChecks {
			case 1:
				return 10, 0, 0, nil
			case 2:
				return 15, 0, 0, nil
			default:
				return 20, 0, 0, nil
			}
		},
	}

	notify.topkCleaner()

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 50 {
		t.Fatalf("remaining entries = %d, want 50", len(entries))
	}
	if diskChecks != 3 {
		t.Fatalf("disk checks = %d, want 3", diskChecks)
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("protected top-k entry was removed: %v", err)
	}
}

func TestTopkCleanerStopsWhenDiskUsageRefreshFails(t *testing.T) {
	cacheDir := t.TempDir()
	createCacheFiles(t, cacheDir, 150)
	diskChecks := 0
	notify := &Notify{
		path:                        cacheDir,
		disk:                        diskutil.NewCache(cacheDir),
		heavykeeper:                 &cleanupTopk{},
		evictUntilPercentBlocksFree: 20,
		getCleanupDiskUsage: func(string) (float64, uint64, uint64, error) {
			diskChecks++
			if diskChecks == 1 {
				return 10, 0, 0, nil
			}
			return 0, 0, 0, errors.New("disk usage unavailable")
		},
	}

	notify.topkCleaner()

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 50 {
		t.Fatalf("remaining entries = %d, want 50", len(entries))
	}
	if diskChecks != 2 {
		t.Fatalf("disk checks = %d, want 2", diskChecks)
	}
}

func createCacheFiles(t *testing.T, cacheDir string, count int) []string {
	t.Helper()
	paths := make([]string, 0, count)
	for i := 0; i < count; i++ {
		path := filepath.Join(cacheDir, fmt.Sprintf("entry-%03d", i))
		if err := os.WriteFile(path, []byte("cache"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
		paths = append(paths, path)
	}
	return paths
}

type cleanupTopk struct {
	items []heavykeeper.Item
}

func (t *cleanupTopk) Add(string, uint32) (string, bool) {
	return "", false
}

func (t *cleanupTopk) List() []heavykeeper.Item {
	return t.items
}

func (t *cleanupTopk) Expelled() <-chan heavykeeper.Item {
	return nil
}

func (t *cleanupTopk) Fading() {}
