package eviction

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hawkingrei/hoshino/diskutil"
	"github.com/hawkingrei/hoshino/eviction/internal/heavykeeper"
	"github.com/hawkingrei/hoshino/eviction/internal/inotify"

	"github.com/sirupsen/logrus"
)

type Notify struct {
	path                     string
	disk                     *diskutil.Cache
	watcher                  *inotify.Watcher
	heavykeeper              heavykeeper.Topk
	transfer                 *transfer
	getCleanupDiskUsage      diskUsageGetter
	policy                   EvictionPolicy
	evicting                 atomic.Bool
	eventStateInvalid        atomic.Bool
	eventStateGeneration     atomic.Uint64
	eventRecoveryMu          sync.Mutex
	eventWatchRescanRequired bool
}

const accessEventBufferSize = 4096

type accessEvent struct {
	key       string
	increment uint32
}

const cleanupBatchSize = 100
const diskUsageRefreshInterval = 5 * time.Minute

type diskUsageGetter func(path string) (percentBlocksFree float64, bytesFree, bytesUsed uint64, err error)

type diskUsageSnapshot struct {
	blocksFree float64
	checkedAt  time.Time
}

func (s *diskUsageSnapshot) refresh(now time.Time, path string, getDiskUsage diskUsageGetter) error {
	if !s.checkedAt.IsZero() {
		age := now.Sub(s.checkedAt)
		if age >= 0 && age < diskUsageRefreshInterval {
			return nil
		}
	}

	blocksFree, _, _, err := getDiskUsage(path)
	if err != nil {
		return err
	}
	s.blocksFree = blocksFree
	s.checkedAt = now
	return nil
}

func New(path, listenPath string, policy EvictionPolicy) (*Notify, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	disk := diskutil.NewCache(path)
	watcher, err := inotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	addDirectoryWatches(watcher, listenPath)
	const HotKeyCnt = 1000_000
	factor := uint32(math.Log(float64(HotKeyCnt)))
	if factor < 1 {
		factor = 1
	}
	heavykeeper := heavykeeper.NewHeavyKeeper(HotKeyCnt, 1024*factor, 4, 0.9, 1)
	return &Notify{
		path:                path,
		transfer:            newTransfer(listenPath, path),
		disk:                disk,
		watcher:             watcher,
		getCleanupDiskUsage: diskutil.GetDiskUsage,
		policy:              policy,
		heavykeeper:         heavykeeper,
	}, nil
}

func addDirectoryWatches(watcher *inotify.Watcher, root string) {
	_ = filepath.Walk(root, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			logrus.WithError(err).Error("error getting some entries")
			return nil
		}
		if f.IsDir() {
			if err := watcher.AddWatch(path, inotify.InOpen|inotify.InCreate|inotify.InIsdir); err != nil {
				logrus.WithError(err).WithField("path", path).Error("failed to add inotify watch")
			}
		}
		return nil
	})
}

func (n *Notify) Start() {
	accesses := make(chan accessEvent, accessEventBufferSize)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		n.runWorker(accesses)
	}()
	defer func() {
		close(accesses)
		<-workerDone
	}()

	watcherErrors := n.watcher.Error
	for {
		select {
		case event, ok := <-n.watcher.Event:
			if !ok {
				return
			}
			if n.eventStateInvalid.Load() {
				continue
			}
			if strings.HasSuffix(event.Name, "/") {
				continue
			}
			if event.Mask&inotify.InIsdir == inotify.InIsdir {
				if event.HasEvent(inotify.InCreate) {
					n.watcher.AddWatch(event.Name, inotify.InOpen|inotify.InCreate|inotify.InIsdir)
				}
				continue
			}
			cache, err := n.transfer.tran(event.Name)
			if err != nil {
				logrus.WithError(err).Error("transfer path")
			}
			access := accessEvent{key: cache, increment: 1}
			if event.HasEvent(inotify.InCreate) {
				access.increment = 10
			}
			n.enqueueAccess(accesses, access)
		case err, ok := <-watcherErrors:
			if !ok {
				watcherErrors = nil
				continue
			}
			if errors.Is(err, inotify.ErrEventOverflow) {
				n.markEventStateInvalid("kernel inotify queue overflow", true)
				continue
			}
			logrus.WithError(err).Error("inotify watcher error")
		}
	}
}

func (n *Notify) runWorker(accesses <-chan accessEvent) {
	ticker := time.NewTicker(n.policy.DiskCheckInterval)
	defer ticker.Stop()
	for {
		if n.eventStateInvalid.Load() {
			if !n.recoverEventState(accesses) {
				return
			}
		}

		select {
		case access, ok := <-accesses:
			if !ok {
				return
			}
			if n.eventStateInvalid.Load() {
				continue
			}
			n.heavykeeper.Add(access.key, access.increment)
		case <-ticker.C:
			n.trickWorker()
		}
	}
}

func (n *Notify) enqueueAccess(accesses chan<- accessEvent, access accessEvent) bool {
	if n.eventStateInvalid.Load() {
		return false
	}
	select {
	case accesses <- access:
		return true
	default:
		n.markEventStateInvalid("internal access-event queue overflow", false)
		return false
	}
}

func (n *Notify) markEventStateInvalid(reason string, rescanWatches bool) {
	n.eventRecoveryMu.Lock()
	wasValid := !n.eventStateInvalid.Load()
	n.eventStateInvalid.Store(true)
	if wasValid {
		n.eventStateGeneration.Add(1)
	}
	n.eventWatchRescanRequired = n.eventWatchRescanRequired || rescanWatches
	n.eventRecoveryMu.Unlock()
	if wasValid {
		logrus.WithField("reason", reason).Warn("Invalidating hot-key state after event loss")
	}
}

func (n *Notify) recoverEventState(accesses <-chan accessEvent) bool {
	n.eventRecoveryMu.Lock()
	defer n.eventRecoveryMu.Unlock()
	if n.eventWatchRescanRequired {
		addDirectoryWatches(n.watcher, n.transfer.listenDir)
	}
	if !drainAccessEvents(accesses) {
		return false
	}
	n.heavykeeper.Reset()
	n.eventWatchRescanRequired = false
	n.eventStateInvalid.Store(false)
	logrus.Warn("Rebuilding hot-key state after event loss")
	return true
}

func drainAccessEvents(accesses <-chan accessEvent) bool {
	for {
		select {
		case _, ok := <-accesses:
			if !ok {
				return false
			}
		default:
			return true
		}
	}
}

func (n *Notify) Background() {
	expelledChan := n.heavykeeper.Expelled()
	usage := diskUsageSnapshot{}
	for {
		generation := n.eventStateGeneration.Load()
		select {
		case item := <-expelledChan:
			if !n.eventStateIsCurrent(generation) {
				continue
			}
			if err := usage.refresh(time.Now(), n.path, diskutil.GetDiskUsage); err != nil {
				logrus.WithError(err).WithField("path", n.path).Error("Failed to get disk usage!")
				continue
			}
			if !n.updateEvictionState(usage.blocksFree) {
				continue
			}
			if !n.eventStateIsCurrent(generation) {
				continue
			}
			logrus.Infof("delete %s from expelledChan", item.Key)
			os.Remove(item.Key)
		}
	}
}

func (n *Notify) eventStateIsCurrent(generation uint64) bool {
	return !n.eventStateInvalid.Load() && generation == n.eventStateGeneration.Load()
}

func (n *Notify) Stop() {
	n.watcher.Close()
}

func (n *Notify) trickWorker() {
	blocksFree, _, _, err := diskutil.GetDiskUsage(n.path)
	if err != nil {
		logrus.WithError(err).WithField("path", n.path).Error("Failed to get disk usage!")
		return
	}
	if n.updateEvictionState(blocksFree) {
		n.topkCleaner()
	}
}

func (n *Notify) topkCleaner() {
	if n.eventStateInvalid.Load() {
		logrus.Warn("skip cache cleanup while hot-key state is invalid")
		return
	}
	blocksFree, _, _, err := n.getCleanupDiskUsage(n.path)
	if err != nil {
		logrus.WithError(err).WithField("path", n.path).Error("Failed to get disk usage!")
		return
	}
	if !n.updateEvictionState(blocksFree) {
		logrus.WithField("blocksFree", blocksFree).Info("disk usage is above the eviction threshold, skip topkCleaner")
		return
	}

	n.heavykeeper.Fading()
	logrus.Infof("topk %d", n.heavykeeper.Len())
	files := n.disk.GetEntries()
	sort.Slice(files, func(i, j int) bool {
		return files[i].LastAccess.Before(files[j].LastAccess)
	})
	deletedSinceCheck := 0
	for _, entry := range files {
		if n.eventStateInvalid.Load() {
			logrus.Warn("stop cache cleanup while hot-key state is invalid")
			return
		}
		if n.heavykeeper.Contains(entry.Path) {
			continue
		}
		if err = n.disk.Delete(n.disk.PathToKey(entry.Path)); err != nil {
			logrus.WithError(err).Errorf("Error deleting entry at path: %v", entry.Path)
			continue
		}
		logrus.Infof("delete %s", entry.Path)
		deletedSinceCheck++
		if deletedSinceCheck < cleanupBatchSize {
			continue
		}

		deletedSinceCheck = 0
		blocksFree, _, _, err = n.getCleanupDiskUsage(n.path)
		if err != nil {
			logrus.WithError(err).WithField("path", n.path).Error("Failed to get disk usage during cleanup!")
			return
		}
		if !n.updateEvictionState(blocksFree) {
			logrus.WithField("blocksFree", blocksFree).Info("eviction target reached")
			return
		}
	}
}

func (n *Notify) updateEvictionState(percentBlocksFree float64) bool {
	for {
		current := n.evicting.Load()
		next := n.policy.nextEvicting(current, percentBlocksFree)
		if current == next || n.evicting.CompareAndSwap(current, next) {
			return next
		}
	}
}
