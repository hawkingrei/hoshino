//go:build linux

package eviction

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hawkingrei/hoshino/diskutil"
	"github.com/hawkingrei/hoshino/eviction/internal/heavykeeper"
	"golang.org/x/sys/unix"
)

func TestRunObservesCompletedEntryAndWritesCheckpoint(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"ac", "cas", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	policy := validPolicy()
	policy.DiskCheckInterval = time.Hour
	policy.CheckpointInterval = time.Hour
	policy.TopKDecayInterval = time.Hour
	checkpointPath := filepath.Join(t.TempDir(), "hot-set.json")
	notify, err := New(Config{
		CacheDir:       root,
		ActivityLock:   filepath.Join(t.TempDir(), "activity.lock"),
		CheckpointFile: checkpointPath,
		Policy:         policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- notify.Run(ctx) }()
	waitFor(t, time.Second, notify.Ready, "notify readiness")

	digest := strings.Repeat("d", 64)
	shard := filepath.Join(root, "cas", "dd")
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(root, "tmp", "completed")
	if err := os.WriteFile(temporary, []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	entryPath := filepath.Join(shard, digest)
	if err := os.Rename(temporary, entryPath); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(entryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	key := "cas/dd/" + digest
	waitFor(t, time.Second, func() bool { return notify.heavykeeper.Contains(key) }, "completed entry observation")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
	items, ok, err := loadCheckpoint(checkpointPath, policy.TopKCapacity)
	if err != nil || !ok || len(items) != 1 || items[0].Key != key {
		t.Fatalf("checkpoint = %#v, %v, %v; want observed key", items, ok, err)
	}
}

func TestRecoverWatcherResetsHeatAndStartsWarmup(t *testing.T) {
	root := t.TempDir()
	notify, err := New(Config{
		CacheDir:       root,
		ActivityLock:   filepath.Join(t.TempDir(), "activity.lock"),
		CheckpointFile: filepath.Join(t.TempDir(), "hot-set.json"),
		Policy:         validPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := notify.reconcile(false); err != nil {
		t.Fatal(err)
	}
	defer notify.watcher.Close()
	notify.heavykeeper.Add("cas/aa/"+strings.Repeat("a", 64), 10)
	before := notify.now()
	if err := notify.recoverWatcher("test overflow"); err != nil {
		t.Fatal(err)
	}
	if notify.heavykeeper.Len() != 0 {
		t.Fatal("watcher recovery retained stale heat metadata")
	}
	if !notify.Ready() || notify.warmUntil.Before(before.Add(notify.policy.WarmupDuration)) {
		t.Fatalf("recovery state = ready %v, warm until %s", notify.Ready(), notify.warmUntil)
	}
}

func TestReconcileDrainsEventsWhileScanning(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("d", 64)
	entryPath := filepath.Join(root, "cas", "dd", digest)
	if err := os.MkdirAll(filepath.Dir(entryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := validPolicy()
	policy.DiskCheckInterval = time.Hour
	policy.CheckpointInterval = time.Hour
	policy.TopKDecayInterval = time.Hour
	notify, err := New(Config{
		CacheDir:       root,
		ActivityLock:   filepath.Join(t.TempDir(), "activity.lock"),
		CheckpointFile: filepath.Join(t.TempDir(), "hot-set.json"),
		Policy:         policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	originalGetEntries := notify.getEntries
	scanStarted := make(chan struct{})
	releaseScan := make(chan struct{})
	var scans atomic.Int32
	notify.getEntries = func() ([]diskutil.EntryInfo, error) {
		if scans.Add(1) == 1 {
			close(scanStarted)
			<-releaseScan
		}
		return originalGetEntries()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- notify.Run(ctx) }()
	select {
	case <-scanStarted:
	case <-time.After(time.Second):
		t.Fatal("initial reconciliation did not start")
	}

	for range watcherEventBufferSizeForTest + 1 {
		file, err := os.Open(entryPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	close(releaseScan)
	waitFor(t, 5*time.Second, notify.Ready, "notify readiness after event burst")
	if got := scans.Load(); got != 1 {
		t.Fatalf("cache scans = %d, want 1 without watcher recovery", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

const watcherEventBufferSizeForTest = 4096

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func TestCleanupShadowModeDoesNotDelete(t *testing.T) {
	notify, entryPath, observer := newCleanupTestNotify(t, true)
	if err := notify.cleanup(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entryPath); err != nil {
		t.Fatalf("shadow mode deleted entry: %v", err)
	}
	if observer.projected == 0 || observer.deleted != 0 {
		t.Fatalf("observer = %#v, want projected bytes without deletion", observer)
	}
}

func TestCleanupSkipsWhileBazelLeaseIsHeld(t *testing.T) {
	notify, entryPath, observer := newCleanupTestNotify(t, false)
	shared, err := os.OpenFile(notify.lease.path, os.O_CREATE|os.O_RDWR, 0o660)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	if err := unix.Flock(int(shared.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(shared.Fd()), unix.LOCK_UN)

	if err := notify.cleanup(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entryPath); err != nil {
		t.Fatalf("active build entry was deleted: %v", err)
	}
	if observer.skippedActive != 1 {
		t.Fatalf("skipped active count = %d, want 1", observer.skippedActive)
	}
}

func TestCleanupDeletesValidatedColdEntry(t *testing.T) {
	notify, entryPath, observer := newCleanupTestNotify(t, false)
	if err := notify.cleanup(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entryPath); !os.IsNotExist(err) {
		t.Fatalf("cold entry still exists: %v", err)
	}
	if observer.deleted == 0 {
		t.Fatal("deletion was not observed")
	}
}

func TestSelectVictimsDoesNotDoubleCountRefreshedFreeSpace(t *testing.T) {
	root := t.TempDir()
	entries := make([]diskutil.EntryInfo, 0, 3)
	for index, letter := range []string{"a", "b", "c"} {
		digest := strings.Repeat(letter, 64)
		key := "cas/" + letter + letter + "/" + digest
		path := filepath.Join(root, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 100), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Duration(index+2) * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	cache, err := diskutil.NewCache(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err = cache.GetEntries()
	if err != nil {
		t.Fatal(err)
	}
	policy := validPolicy()
	policy.CleanupBatchSize = 1
	policy.MinimumResidence = 0
	policy.MaxCacheBytes = 1
	policy.TargetCacheBytes = 0
	policy.MinActionCacheBytes = 0
	checks := 0
	observer := &testObserver{}
	notify := &Notify{
		cache:       cache,
		heavykeeper: heavykeeper.NewHeavyKeeper(10, 128, 2, 0.9, 1),
		policy:      policy,
		observer:    observer,
		getDiskUsage: func(string) (float64, uint64, uint64, error) {
			checks++
			free := uint64(100 + (checks * 100))
			used := uint64(1000) - free
			return float64(free) / 10, free, used, nil
		},
	}
	notify.evicting = true
	deleted := notify.selectVictims(entries, time.Now(), 100, 900, true)
	if deleted != 300 || observer.deleted != 300 {
		t.Fatalf("deleted bytes = %d, observed = %d; want 300", deleted, observer.deleted)
	}
}

func TestSelectVictimsRanksHeatBeforeAge(t *testing.T) {
	root := t.TempDir()
	keys := []string{
		"cas/aa/" + strings.Repeat("a", 64),
		"cas/bb/" + strings.Repeat("b", 64),
	}
	for index, key := range keys {
		path := filepath.Join(root, filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 100), 0o644); err != nil {
			t.Fatal(err)
		}
		age := time.Duration(3-index) * time.Hour
		if err := os.Chtimes(path, time.Now().Add(-age), time.Now().Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	cache, err := diskutil.NewCache(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := cache.GetEntries()
	if err != nil {
		t.Fatal(err)
	}
	policy := validPolicy()
	policy.MinimumResidence = 0
	policy.MaxCacheBytes = 150
	policy.TargetCacheBytes = 100
	policy.MinActionCacheBytes = 0
	topk := heavykeeper.NewHeavyKeeper(10, 128, 2, 0.9, 1)
	topk.Add(keys[0], 10)
	notify := &Notify{
		cache:        cache,
		heavykeeper:  topk,
		policy:       policy,
		observer:     &testObserver{},
		getDiskUsage: func(string) (float64, uint64, uint64, error) { return 50, 500, 500, nil },
	}
	notify.evicting = true
	if deleted := notify.selectVictims(entries, time.Now(), 500, 500, true); deleted != 100 {
		t.Fatalf("deleted bytes = %d, want 100", deleted)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(keys[0]))); err != nil {
		t.Fatalf("hotter entry was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(keys[1]))); !os.IsNotExist(err) {
		t.Fatalf("colder entry remains: %v", err)
	}
}

func newCleanupTestNotify(t *testing.T, shadow bool) (*Notify, string, *testObserver) {
	t.Helper()
	root := t.TempDir()
	digest := strings.Repeat("a", 64)
	key := "cas/aa/" + digest
	entryPath := filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(entryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(entryPath, old, old); err != nil {
		t.Fatal(err)
	}
	cache, err := diskutil.NewCache(root)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := NewActivityLease(filepath.Join(t.TempDir(), "activity.lock"))
	if err != nil {
		t.Fatal(err)
	}
	policy := validPolicy()
	policy.Shadow = shadow
	policy.MaxCacheBytes = 1
	policy.TargetCacheBytes = 0
	policy.MinActionCacheBytes = 0
	observer := &testObserver{}
	notify := &Notify{
		cache:          cache,
		heavykeeper:    heavykeeper.NewHeavyKeeper(10, 128, 2, 0.9, 1),
		lease:          lease,
		policy:         policy,
		observer:       observer,
		now:            time.Now,
		getDiskUsage:   func(string) (float64, uint64, uint64, error) { return 50, 1 << 30, 1 << 30, nil },
		checkpointFile: filepath.Join(t.TempDir(), "hot-set.json"),
	}
	notify.ready.Store(true)
	return notify, entryPath, observer
}

type testObserver struct {
	projected     int64
	deleted       int64
	skippedActive int
}

func (*testObserver) SetDisk(uint64, uint64)          {}
func (*testObserver) SetCacheBytes(int64)             {}
func (*testObserver) SetReady(bool)                   {}
func (*testObserver) RecordAccess(diskutil.EntryKind) {}
func (*testObserver) RecordClose(diskutil.EntryKind)  {}
func (o *testObserver) RecordProjected(bytes int64)   { o.projected += bytes }
func (o *testObserver) RecordDeleted(_ diskutil.EntryKind, bytes int64) {
	o.deleted += bytes
}
func (o *testObserver) RecordSkippedActive() { o.skippedActive++ }
func (*testObserver) RecordFrozen(string)    {}
func (*testObserver) RecordReconciliation()  {}
