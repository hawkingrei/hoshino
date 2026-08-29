package eviction

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hawkingrei/hoshino/eviction/internal/heavykeeper"
)

func TestCheckpointRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "hot-set.json")
	digest := strings.Repeat("a", 64)
	want := []heavykeeper.Item{{Key: "cas/aa/" + digest, Count: 7}}
	now := time.Date(2026, time.August, 29, 10, 0, 0, 0, time.UTC)
	if err := saveCheckpoint(path, want, now); err != nil {
		t.Fatal(err)
	}
	got, ok, err := loadCheckpoint(path, 10)
	if err != nil || !ok || len(got) != 1 || got[0] != want[0] {
		t.Fatalf("loadCheckpoint() = %#v, %v, %v; want %#v, true, nil", got, ok, err, want)
	}
}

func TestCheckpointFailsClosedOnInvalidKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hot-set.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"saved_at":"2026-08-29T00:00:00Z","items":[{"Key":"../../outside","Count":1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := loadCheckpoint(path, 10); err == nil || ok {
		t.Fatalf("loadCheckpoint() = ok %v, error %v; want fail-closed error", ok, err)
	}
}
