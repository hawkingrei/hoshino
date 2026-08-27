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
	path        string
	disk        *diskutil.Cache
	watcher     *inotify.Watcher
	write       atomic.Int64
	heavykeeper heavykeeper.Topk
	transfer    *transfer

	minPercentBlocksFree        float64
	evictUntilPercentBlocksFree float64
	eventStateInvalid           atomic.Bool
	eventRecoveryMu             sync.Mutex
	eventWatchRescanRequired    bool
}

const accessEventBufferSize = 4096

type accessEvent struct {
	key       string
	increment uint32
	write     bool
}

func New(path, listenPath string, minPercentBlocksFree, evictUntilPercentBlocksFree float64) *Notify {
	disk := diskutil.NewCache(path)
	watcher, err := inotify.NewWatcher()
	if err != nil {
		logrus.Fatal(err)
	}
	addDirectoryWatches(watcher, listenPath)
	const HotKeyCnt = 1000_000
	factor := uint32(math.Log(float64(HotKeyCnt)))
	if factor < 1 {
		factor = 1
	}
	heavykeeper := heavykeeper.NewHeavyKeeper(HotKeyCnt, 1024*factor, 4, 0.9, 1)
	return &Notify{
		path:                        path,
		transfer:                    newTransfer(listenPath, path),
		disk:                        disk,
		watcher:                     watcher,
		minPercentBlocksFree:        minPercentBlocksFree,
		evictUntilPercentBlocksFree: evictUntilPercentBlocksFree,
		heavykeeper:                 heavykeeper,
	}
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
				access.write = true
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
	ticker := time.NewTicker(15 * time.Minute)
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
			if access.write {
				n.write.Add(1)
			}
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
	n.write.Store(0)
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
	now := time.Now()
	blocksFree := 0.0
	var err error
	for {
		select {
		case item := <-expelledChan:
			if n.eventStateInvalid.Load() {
				continue
			}
			if time.Since(now) > 5*time.Minute {
				now = time.Now()
				blocksFree, _, _, err = diskutil.GetDiskUsage(n.path)
				if err != nil {
					logrus.WithError(err).WithField("path", n.path).Error("Failed to get disk usage!")
				}
			}
			if blocksFree > 50 {
				continue
			}
			if n.eventStateInvalid.Load() {
				continue
			}
			logrus.Infof("delete %s from expelledChan", item.Key)
			os.Remove(item.Key)
		}
	}
	return
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
	var value int64 = 0
	if blocksFree > 70 {
		value = 35000
	} else {
		value = 15000
	}
	if n.write.Load() > value {
		n.write.Store(0)
		n.topkCleaner()
	}
}

func (n *Notify) topkCleaner() {
	n.heavykeeper.Fading()
	top := n.heavykeeper.List()
	topset := make(map[string]uint32)
	for _, item := range top {
		topset[item.Key] = item.Count
	}

	blocksFree, _, _, err := diskutil.GetDiskUsage(n.path)
	if err != nil {
		logrus.WithError(err).WithField("path", n.path).Error("Failed to get disk usage!")
		return
	}
	logrus.Infof("topk %d", len(top))
	if blocksFree > 30 {
		logrus.WithField("blocksFree", blocksFree).Info("blocksFree > 70, skip topkCleaner")
		return
	}
	files := n.disk.GetEntries()
	sort.Slice(files, func(i, j int) bool {
		return files[i].LastAccess.Before(files[j].LastAccess)
	})
	for _, entry := range files {
		_, ok := topset[entry.Path]
		if !ok {
			err = n.disk.Delete(n.disk.PathToKey(entry.Path))
			if err != nil {
				logrus.WithError(err).Errorf("Error deleting entry at path: %v", entry.Path)
			} else {
				logrus.Infof("delete %s", entry.Path)
			}
		}
	}
}
