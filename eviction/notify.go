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
	eventCnt    atomic.Int64
	write       atomic.Int64
	heavykeeper heavykeeper.Topk
	transfer    *transfer

	minPercentBlocksFree        float64
	evictUntilPercentBlocksFree float64
}

func New(path, listenPath string, minPercentBlocksFree, evictUntilPercentBlocksFree float64) *Notify {
	disk := diskutil.NewCache(path)
	watcher, err := inotify.NewWatcher()
	if err != nil {
		logrus.Fatal(err)
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
	const HotKeyCnt = 20000000
	factor := uint32(math.Log(float64(HotKeyCnt)))
	if factor < 1 {
		factor = 1
	}
	heavykeeper := heavykeeper.NewHeavyKeeper(HotKeyCnt, 1024*factor, 4, 0.925, 1024)
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

func (n *Notify) Start() {
	expelledChan := n.heavykeeper.Expelled()
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case event, ok := <-n.watcher.Event:
			if !ok {
				return
			}
			if strings.HasSuffix(event.Name, "/") {
				//logrus.WithField("event", event).Info("skip Got event")
				continue
			}
			n.eventCnt.Add(1)
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
				logrus.Infof("create %s", event.Name)
				n.heavykeeper.Add(cache, 10)
				n.write.Add(1)
			} else {
				logrus.Infof("open %s", event.Name)
				n.heavykeeper.Add(cache, 1)
			}
		case <-ticker.C:
			n.trickWorker()
		case item := <-expelledChan:
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
	if n.eventCnt.Load() > 10000 && n.write.Load() > 10000 {
		n.eventCnt.Store(0)
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
				logrus.Info("delete %s", entry.Path)
			}
		}
		newBlockFree, _, _, err := diskutil.GetDiskUsage(n.path)
		if err != nil {
			logrus.WithError(err).Error("Failed to get disk usage!")
			continue
		}
		if blocksFree-newBlockFree >= 1 {
			break
		}
	}
}
