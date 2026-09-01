//go:build linux

package eviction

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hawkingrei/hoshino/diskutil"
	"github.com/hawkingrei/hoshino/eviction/internal/heavykeeper"
	"github.com/hawkingrei/hoshino/eviction/internal/inotify"
	"github.com/sirupsen/logrus"
)

const watchMask = inotify.InOpen |
	inotify.InClose |
	inotify.InCreate |
	inotify.InDelete |
	inotify.InMove |
	inotify.InDeleteSelf |
	inotify.InMoveSelf |
	inotify.InDontFollow

type Notify struct {
	cache          *diskutil.Cache
	watcher        *inotify.Watcher
	heavykeeper    heavykeeper.Topk
	lease          *ActivityLease
	policy         EvictionPolicy
	checkpointFile string
	observer       Observer
	ready          atomic.Bool
	warmUntil      time.Time
	evicting       bool
	now            func() time.Time
	getDiskUsage   diskUsageGetter
	getEntries     cacheEntriesGetter
}

type diskUsageGetter func(path string) (percentBlocksFree float64, bytesFree, bytesUsed uint64, err error)
type cacheEntriesGetter func() ([]diskutil.EntryInfo, error)

func New(config Config) (*Notify, error) {
	if err := config.Policy.Validate(); err != nil {
		return nil, err
	}
	if config.CacheDir == "" || config.CheckpointFile == "" {
		return nil, errors.New("cache directory and checkpoint file must not be empty")
	}
	cache, err := diskutil.NewCache(config.CacheDir)
	if err != nil {
		return nil, err
	}
	lease, err := NewActivityLease(config.ActivityLock)
	if err != nil {
		return nil, err
	}
	observer := config.Observer
	if observer == nil {
		observer = noopObserver{}
	}
	return &Notify{
		cache: cache,
		heavykeeper: heavykeeper.NewHeavyKeeper(
			config.Policy.TopKCapacity,
			config.Policy.TopKWidth,
			config.Policy.TopKDepth,
			config.Policy.TopKDecay,
			config.Policy.TopKMinCount,
		),
		lease:          lease,
		policy:         config.Policy,
		checkpointFile: config.CheckpointFile,
		observer:       observer,
		now:            time.Now,
		getDiskUsage:   diskutil.GetDiskUsage,
		getEntries:     cache.GetEntries,
	}, nil
}

func (n *Notify) Ready() bool {
	return n.ready.Load()
}

func (n *Notify) Run(ctx context.Context) error {
	checkpointRestored := n.restoreCheckpoint()
	if err := n.reconcile(false); err != nil {
		return err
	}
	if !checkpointRestored {
		n.warmUntil = n.now().Add(n.policy.WarmupDuration)
	}
	defer func() {
		n.ready.Store(false)
		n.observer.SetReady(false)
		if n.watcher != nil {
			_ = n.watcher.Close()
		}
	}()

	cleanupTicker := time.NewTicker(n.policy.DiskCheckInterval)
	checkpointTicker := time.NewTicker(n.policy.CheckpointInterval)
	decayTicker := time.NewTicker(n.policy.TopKDecayInterval)
	defer cleanupTicker.Stop()
	defer checkpointTicker.Stop()
	defer decayTicker.Stop()

	for {
		eventChannel := n.watcher.Event
		errorChannel := n.watcher.Error
		select {
		case <-ctx.Done():
			if err := n.saveCheckpoint(); err != nil {
				logrus.WithError(err).Error("Failed to save final hot-set checkpoint")
			}
			return nil
		case event, ok := <-eventChannel:
			if !ok {
				if err := n.recoverWatcher("event channel closed"); err != nil {
					return err
				}
				continue
			}
			if err := n.handleEvent(event); err != nil {
				if err := n.recoverWatcher(err.Error()); err != nil {
					return err
				}
			}
		case err, ok := <-errorChannel:
			if !ok {
				if err := n.recoverWatcher("watcher error channel closed"); err != nil {
					return err
				}
				continue
			}
			if errors.Is(err, inotify.ErrEventOverflow) {
				if err := n.recoverWatcher("inotify queue overflow"); err != nil {
					return err
				}
				continue
			}
			logrus.WithError(err).Error("Inotify watcher error")
		case <-cleanupTicker.C:
			if err := n.cleanup(n.now()); err != nil {
				logrus.WithError(err).Error("Cache cleanup failed")
			}
		case <-checkpointTicker.C:
			if err := n.saveCheckpoint(); err != nil {
				logrus.WithError(err).Error("Failed to save hot-set checkpoint")
			}
		case <-decayTicker.C:
			n.heavykeeper.Fading()
		}
	}
}

func (n *Notify) restoreCheckpoint() bool {
	items, ok, err := loadCheckpoint(n.checkpointFile, n.policy.TopKCapacity)
	if err != nil {
		logrus.WithError(err).Warn("Ignoring invalid hot-set checkpoint")
		return false
	}
	if !ok {
		return false
	}
	existing := make([]heavykeeper.Item, 0, len(items))
	for _, item := range items {
		if _, err := n.cache.Snapshot(item.Key); err == nil {
			existing = append(existing, item)
		}
	}
	n.heavykeeper.Restore(existing)
	return true
}

func (n *Notify) saveCheckpoint() error {
	return saveCheckpoint(n.checkpointFile, n.heavykeeper.List(), n.now())
}

func (n *Notify) reconcile(resetHeat bool) error {
	n.ready.Store(false)
	n.observer.SetReady(false)
	if n.watcher != nil {
		_ = n.watcher.Close()
	}
	watcher, err := inotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create inotify watcher: %w", err)
	}
	if err := n.addDirectoryWatches(watcher, n.cache.DiskRoot()); err != nil {
		_ = watcher.Close()
		return fmt.Errorf("rebuild inotify watches: %w", err)
	}
	entries, err := n.scanEntriesWhileDraining(watcher)
	if err != nil {
		_ = watcher.Close()
		return err
	}
	n.watcher = watcher
	if resetHeat {
		n.heavykeeper.Reset()
		n.warmUntil = n.now().Add(n.policy.WarmupDuration)
	}
	n.observer.SetCacheBytes(totalBytes(entries))
	n.observer.RecordReconciliation()
	n.ready.Store(true)
	n.observer.SetReady(true)
	return nil
}

func (n *Notify) scanEntriesWhileDraining(watcher *inotify.Watcher) ([]diskutil.EntryInfo, error) {
	stop := make(chan struct{})
	drainDone := make(chan error, 1)
	go func() {
		drainDone <- n.drainReconciliationEvents(stop, watcher)
	}()

	entries, scanErr := n.getEntries()
	close(stop)
	drainErr := <-drainDone
	if scanErr != nil {
		return nil, fmt.Errorf("rescan disk cache: %w", scanErr)
	}
	if drainErr != nil {
		return nil, drainErr
	}
	return entries, nil
}

func (n *Notify) drainReconciliationEvents(stop <-chan struct{}, watcher *inotify.Watcher) error {
	for {
		select {
		case <-stop:
			return nil
		case event, ok := <-watcher.Event:
			if !ok {
				return errors.New("inotify event channel closed during reconciliation")
			}
			if err := n.handleReconciliationEvent(watcher, event); err != nil {
				return err
			}
		case err, ok := <-watcher.Error:
			if !ok {
				return errors.New("inotify error channel closed during reconciliation")
			}
			if errors.Is(err, inotify.ErrEventOverflow) {
				return fmt.Errorf("inotify queue overflow during reconciliation: %w", err)
			}
			return fmt.Errorf("inotify watcher error during reconciliation: %w", err)
		}
	}
}

func (n *Notify) handleReconciliationEvent(watcher *inotify.Watcher, event *inotify.Event) error {
	if event == nil {
		return nil
	}
	if event.HasEvent(inotify.InUnmount) || event.HasEvent(inotify.InIgnored) ||
		event.HasEvent(inotify.InDeleteSelf) || event.HasEvent(inotify.InMoveSelf) {
		return fmt.Errorf("watch coverage changed during reconciliation for %s", event.Name)
	}
	if event.HasEvent(inotify.InIsdir) &&
		(event.HasEvent(inotify.InCreate) || event.HasEvent(inotify.InMovedTo)) {
		if err := n.addDirectoryWatches(watcher, filepath.Clean(event.Name)); err != nil {
			return fmt.Errorf("watch new cache directory during reconciliation: %w", err)
		}
	}
	return nil
}

func (n *Notify) recoverWatcher(reason string) error {
	n.observer.RecordFrozen("watcher_loss")
	logrus.WithField("reason", reason).Warn("Freezing deletion and rebuilding watcher state")
	return n.reconcile(true)
}

func (n *Notify) addDirectoryWatches(watcher *inotify.Watcher, start string) error {
	if !n.watchPathAllowed(start) {
		return nil
	}
	return filepath.WalkDir(start, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(n.cache.DiskRoot(), path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return filepath.SkipDir
		}
		if relative != "." {
			first := strings.Split(filepath.ToSlash(relative), "/")[0]
			if first != string(diskutil.ActionCache) && first != string(diskutil.ContentCAS) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if err := watcher.AddWatch(path, watchMask); err != nil {
			return err
		}
		return nil
	})
}

func (n *Notify) watchPathAllowed(path string) bool {
	relative, err := filepath.Rel(n.cache.DiskRoot(), filepath.Clean(path))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return false
	}
	if relative == "." {
		return true
	}
	first := strings.Split(filepath.ToSlash(relative), "/")[0]
	return first == string(diskutil.ActionCache) || first == string(diskutil.ContentCAS)
}

func (n *Notify) handleEvent(event *inotify.Event) error {
	if event == nil {
		return nil
	}
	if event.HasEvent(inotify.InUnmount) || event.HasEvent(inotify.InIgnored) ||
		event.HasEvent(inotify.InDeleteSelf) || event.HasEvent(inotify.InMoveSelf) {
		return fmt.Errorf("watch coverage changed for %s", event.Name)
	}
	if event.HasEvent(inotify.InIsdir) {
		if event.HasEvent(inotify.InCreate) || event.HasEvent(inotify.InMovedTo) {
			if err := n.addDirectoryWatches(n.watcher, filepath.Clean(event.Name)); err != nil {
				return fmt.Errorf("watch new cache directory: %w", err)
			}
			if err := n.seedExistingEntries(filepath.Clean(event.Name)); err != nil {
				return fmt.Errorf("seed new cache directory: %w", err)
			}
		}
		return nil
	}

	key, kind, err := n.cache.KeyForPath(event.Name)
	if err != nil {
		return nil
	}
	if event.HasEvent(inotify.InDelete) || event.HasEvent(inotify.InMovedFrom) {
		n.heavykeeper.Remove(key)
		return nil
	}
	if event.Mask&inotify.InClose != 0 {
		n.observer.RecordClose(kind)
	}
	if !event.HasEvent(inotify.InOpen) && !event.HasEvent(inotify.InMovedTo) {
		return nil
	}
	// Access events only influence heat ranking. Cache cleanup snapshots and
	// revalidates entries before deletion, so avoid filesystem I/O on this hot path.
	n.heavykeeper.Add(key, 1)
	if event.HasEvent(inotify.InOpen) {
		n.observer.RecordAccess(kind)
	}
	return nil
}

func (n *Notify) seedExistingEntries(root string) error {
	if !n.watchPathAllowed(root) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		key, _, err := n.cache.KeyForPath(path)
		if err != nil {
			return nil
		}
		if _, err := n.cache.Snapshot(key); err != nil {
			return nil
		}
		n.heavykeeper.Add(key, 1)
		return nil
	})
}

func (n *Notify) cleanup(now time.Time) error {
	n.observer.RecordProjected(0)
	entries, err := n.cache.GetEntries()
	if err != nil {
		return err
	}
	percentFree, bytesFree, bytesUsed, err := n.getDiskUsage(n.cache.DiskRoot())
	if err != nil {
		return err
	}
	cacheBytes := totalBytes(entries)
	n.observer.SetDisk(bytesFree, bytesUsed)
	n.observer.SetCacheBytes(cacheBytes)
	snapshot := pressureSnapshot{percentBlocksFree: percentFree, cacheBytes: cacheBytes}
	n.evicting = n.policy.nextEvicting(n.evicting, snapshot)
	if !n.evicting {
		return nil
	}
	if !n.Ready() {
		n.observer.RecordFrozen("not_ready")
		return nil
	}
	if now.Before(n.warmUntil) {
		n.observer.RecordFrozen("warmup")
		return nil
	}

	if n.policy.Shadow {
		projected := n.selectVictims(entries, now, bytesFree, bytesUsed, false)
		n.observer.RecordProjected(projected)
		return nil
	}

	release, acquired, err := n.lease.TryExclusive()
	if err != nil {
		return err
	}
	if !acquired {
		n.observer.RecordSkippedActive()
		return nil
	}
	defer func() {
		if err := release(); err != nil {
			logrus.WithError(err).Error("Failed to release activity lock")
		}
	}()

	entries, err = n.cache.GetEntries()
	if err != nil {
		return err
	}
	percentFree, bytesFree, bytesUsed, err = n.getDiskUsage(n.cache.DiskRoot())
	if err != nil {
		return err
	}
	cacheBytes = totalBytes(entries)
	n.evicting = n.policy.nextEvicting(n.evicting, pressureSnapshot{
		percentBlocksFree: percentFree,
		cacheBytes:        cacheBytes,
	})
	if !n.evicting {
		return nil
	}
	n.selectVictims(entries, now, bytesFree, bytesUsed, true)
	return nil
}

func (n *Notify) selectVictims(entries []diskutil.EntryInfo, now time.Time, bytesFree, bytesUsed uint64, deleteEntries bool) int64 {
	heat := make(map[string]uint32, n.heavykeeper.Len())
	for _, item := range n.heavykeeper.List() {
		heat[item.Key] = item.Count
	}
	sort.Slice(entries, func(i, j int) bool {
		iHeat := heat[entries[i].Key]
		jHeat := heat[entries[j].Key]
		if iHeat != jHeat {
			return iHeat < jHeat
		}
		if !entries[i].ModTime.Equal(entries[j].ModTime) {
			return entries[i].ModTime.Before(entries[j].ModTime)
		}
		return entries[i].Size > entries[j].Size
	})

	cacheBytes := totalBytes(entries)
	actionCacheBytes := bytesByKind(entries, diskutil.ActionCache)
	projectedBytes := int64(0)
	bytesSinceCheck := int64(0)
	deletedSinceCheck := 0
	for _, entry := range entries {
		if entry.ModTime.After(now.Add(-n.policy.MinimumResidence)) {
			continue
		}
		if entry.Kind == diskutil.ActionCache && actionCacheBytes-entry.Size < n.policy.MinActionCacheBytes {
			continue
		}
		if deleteEntries {
			if err := n.cache.Delete(entry); err != nil {
				if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, diskutil.ErrEntryChanged) {
					logrus.WithError(err).WithField("key", entry.Key).Warn("Skipping cache entry deletion")
				}
				continue
			}
			n.heavykeeper.Remove(entry.Key)
			n.observer.RecordDeleted(entry.Kind, entry.Size)
			deletedSinceCheck++
		}
		projectedBytes += entry.Size
		bytesSinceCheck += entry.Size
		cacheBytes -= entry.Size
		if entry.Kind == diskutil.ActionCache {
			actionCacheBytes -= entry.Size
		}
		simulatedFree := bytesFree + uint64(bytesSinceCheck)
		percentFree := float64(simulatedFree) / float64(bytesFree+bytesUsed) * 100
		if deleteEntries && deletedSinceCheck >= n.policy.CleanupBatchSize {
			actualPercent, actualFree, actualUsed, err := n.getDiskUsage(n.cache.DiskRoot())
			if err != nil {
				logrus.WithError(err).Error("Stopping cleanup after disk usage refresh failed")
				return projectedBytes
			}
			bytesFree, bytesUsed = actualFree, actualUsed
			percentFree = actualPercent
			bytesSinceCheck = 0
			deletedSinceCheck = 0
		}
		if !n.policy.nextEvicting(true, pressureSnapshot{
			percentBlocksFree: percentFree,
			cacheBytes:        cacheBytes,
		}) {
			if deleteEntries {
				actualPercent, _, _, err := n.getDiskUsage(n.cache.DiskRoot())
				if err != nil {
					logrus.WithError(err).Error("Stopping cleanup before final disk usage confirmation")
					return projectedBytes
				}
				if n.policy.nextEvicting(true, pressureSnapshot{
					percentBlocksFree: actualPercent,
					cacheBytes:        cacheBytes,
				}) {
					continue
				}
				n.evicting = false
			}
			break
		}
	}
	return projectedBytes
}

func totalBytes(entries []diskutil.EntryInfo) int64 {
	var total int64
	for _, entry := range entries {
		total += entry.Size
	}
	return total
}

func bytesByKind(entries []diskutil.EntryInfo, kind diskutil.EntryKind) int64 {
	var total int64
	for _, entry := range entries {
		if entry.Kind == kind {
			total += entry.Size
		}
	}
	return total
}
