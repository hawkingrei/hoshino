package fsnotify

import (
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/utils/inotify"
)

type notify struct {
	watcher *inotify.Watcher
}

func New(path string) *notify {
	watcher, err := inotify.NewWatcher()
	if err != nil {
		logrus.Fatal(err)
	}
	watcher.AddWatch(path, inotify.InOpen)
	return &notify{
		watcher: watcher,
	}
}

func (n *notify) Start() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case event, ok := <-n.watcher.Event:
			if !ok {
				return
			}
			logrus.Info("event:", event.Name)
		case <-	ticker.C:
	}
	return
}

