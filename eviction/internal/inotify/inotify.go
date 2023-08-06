package inotify

import (
	"sync"
)

// Event represents a notification
type Event struct {
	Mask   uint32 // Mask of events
	Cookie uint32 // Unique cookie associating related events (for rename(2))
	Name   string // File name (optional)
}

func (e *Event) HasEvent(h uint32) bool {
	return e.Mask&h == h
}

type watch struct {
	wd    uint32 // Watch descriptor (as returned by the inotify_add_watch() syscall)
	flags uint32 // inotify flags of this watch (see inotify(7) for the list of valid flags)
}

// Watcher represents an inotify instance
type Watcher struct {
	mu       sync.Mutex
	fd       int               // File descriptor (as returned by the inotify_init() syscall)
	watches  map[string]*watch // Map of inotify watches (key: path)
	paths    map[int]string    // Map of watched paths (key: watch descriptor)
	Error    chan error        // Errors are sent on this channel
	Event    chan *Event       // Events are returned on this channel
	done     chan bool         // Channel for sending a "quit message" to the reader goroutine
	isClosed bool              // Set to true when Close() is first called
}
