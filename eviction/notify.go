package eviction

import (
	"math"
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

type Notify struct {
	path        string
	disk        *diskutil.Cache
	watcher     *inotify.Watcher
	heavykeeper heavykeeper.Topk
	transfer    *transfer
	policy      EvictionPolicy
	evicting    atomic.Bool
}

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
	filepath.Walk(listenPath, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			logrus.WithError(err).Error("error getting some entries")
			return nil
		}
		if f.IsDir() {
			watcher.AddWatch(path, inotify.InOpen|inotify.InCreate|inotify.InIsdir)
		}
		return nil
	})
	const HotKeyCnt = 1000_000
	factor := uint32(math.Log(float64(HotKeyCnt)))
	if factor < 1 {
		factor = 1
	}
	heavykeeper := heavykeeper.NewHeavyKeeper(HotKeyCnt, 1024*factor, 4, 0.9, 1)
	return &Notify{
		path:        path,
		transfer:    newTransfer(listenPath, path),
		disk:        disk,
		watcher:     watcher,
		policy:      policy,
		heavykeeper: heavykeeper,
	}, nil
}

func (n *Notify) Start() {
	ticker := time.NewTicker(n.policy.DiskCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case event, ok := <-n.watcher.Event:
			if !ok {
				return
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
			if event.HasEvent(inotify.InCreate) {
				n.heavykeeper.Add(cache, 10)
			} else {
				n.heavykeeper.Add(cache, 1)
			}
		case <-ticker.C:
			n.trickWorker()
		}
	}
	return
}

func (n *Notify) Background() {
	expelledChan := n.heavykeeper.Expelled()
	usage := diskUsageSnapshot{}
	for {
		select {
		case item := <-expelledChan:
			if err := usage.refresh(time.Now(), n.path, diskutil.GetDiskUsage); err != nil {
				logrus.WithError(err).WithField("path", n.path).Error("Failed to get disk usage!")
				continue
			}
			if !n.updateEvictionState(usage.blocksFree) {
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
	if n.updateEvictionState(blocksFree) {
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
	if !n.updateEvictionState(blocksFree) {
		logrus.WithField("blocksFree", blocksFree).Info("disk usage is above the eviction threshold, skip topkCleaner")
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

func (n *Notify) updateEvictionState(percentBlocksFree float64) bool {
	for {
		current := n.evicting.Load()
		next := n.policy.nextEvicting(current, percentBlocksFree)
		if current == next || n.evicting.CompareAndSwap(current, next) {
			return next
		}
	}
}
