//go:build linux
// +build linux

// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in third_party/go/LICENSE.

/*
Package inotify implements a wrapper for the Linux inotify system.

Example:

	watcher, err := inotify.NewWatcher()
	if err != nil {
	    log.Fatal(err)
	}
	err = watcher.Watch("/tmp")
	if err != nil {
	    log.Fatal(err)
	}
	for {
	    select {
	    case ev := <-watcher.Event:
	        log.Println("event:", ev)
	    case err := <-watcher.Error:
	        log.Println("error:", err)
	    }
	}
*/
package inotify // import "k8s.io/utils/inotify"

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const watcherEventBufferSize = 4096

// NewWatcher creates and returns a new inotify instance using inotify_init(2)
func NewWatcher() (*Watcher, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, os.NewSyscallError("inotify_init", err)
	}
	w := &Watcher{
		fd:      fd,
		watches: make(map[string]*watch),
		paths:   make(map[int]string),
		Event:   make(chan *Event, watcherEventBufferSize),
		Error:   make(chan error, watcherEventBufferSize),
		done:    make(chan struct{}),
	}

	go w.readEvents()
	return w, nil
}

// Close closes an inotify watcher instance
// It sends a message to the reader goroutine to quit and removes all watches
// associated with the inotify instance
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.isClosed {
		w.mu.Unlock()
		return nil
	}
	w.isClosed = true
	close(w.done)
	w.mu.Unlock()
	return nil
}

// AddWatch adds path to the watched file set.
// The flags are interpreted as described in inotify_add_watch(2).
func (w *Watcher) AddWatch(path string, flags uint32) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.isClosed {
		return errors.New("inotify instance already closed")
	}

	watchEntry, found := w.watches[path]
	if found {
		watchEntry.flags |= flags
		flags |= syscall.IN_MASK_ADD
	}

	wd, err := syscall.InotifyAddWatch(w.fd, path, flags)
	if err != nil {
		return &os.PathError{
			Op:   "inotify_add_watch",
			Path: path,
			Err:  err,
		}
	}

	if !found {
		w.watches[path] = &watch{wd: uint32(wd), flags: flags}
		w.paths[wd] = path
	}
	return nil
}

// Watch adds path to the watched file set, watching all events.
func (w *Watcher) Watch(path string) error {
	return w.AddWatch(path, InAllEvents)
}

// RemoveWatch removes path from the watched file set.
func (w *Watcher) RemoveWatch(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	watch, ok := w.watches[path]
	if !ok {
		return fmt.Errorf("can't remove non-existent inotify watch for: %s", path)
	}
	success, errno := syscall.InotifyRmWatch(w.fd, watch.wd)
	if success == -1 {
		// when file descriptor or watch descriptor not found, InotifyRmWatch syscall return EINVAL error
		// if return error, it may lead this path remain in watches and paths map, and no other event can trigger remove action.
		if errno != syscall.EINVAL {
			return os.NewSyscallError("inotify_rm_watch", errno)
		}
	}
	delete(w.watches, path)
	delete(w.paths, int(watch.wd))
	return nil
}

// readEvents reads from the inotify file descriptor, converts the
// received events into Event objects and sends them via the Event channel
func (w *Watcher) readEvents() {
	var buf [syscall.SizeofInotifyEvent * 4096]byte
	defer syscall.Close(w.fd)
	defer close(w.Event)
	defer close(w.Error)

	for {
		if !w.waitReadable() {
			return
		}
		n, err := syscall.Read(w.fd, buf[:])
		select {
		case <-w.done:
			return
		default:
		}
		if n == 0 {
			return
		}
		if n < 0 {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EINTR) {
				continue
			}
			w.reportError(os.NewSyscallError("read", err))
			continue
		}
		if n < syscall.SizeofInotifyEvent {
			w.reportError(errors.New("inotify: short read in readEvents()"))
			continue
		}

		var offset uint32
		// We don't know how many events we just read into the buffer
		// While the offset points to at least one whole event...
		for offset <= uint32(n-syscall.SizeofInotifyEvent) {
			// Point "raw" to the event in the buffer
			raw := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			event := new(Event)
			event.Mask = uint32(raw.Mask)
			event.Cookie = uint32(raw.Cookie)
			nameLen := uint32(raw.Len)
			if w.reportOverflow(event.Mask) {
				offset += syscall.SizeofInotifyEvent + nameLen
				continue
			}
			// If the event happened to the watched directory or the watched file, the kernel
			// doesn't append the filename to the event, but we would like to always fill the
			// the "Name" field with a valid filename. We retrieve the path of the watch from
			// the "paths" map.
			w.mu.Lock()
			name, ok := w.paths[int(raw.Wd)]
			w.mu.Unlock()
			if ok {
				event.Name = name
				if nameLen > 0 {
					// Point "bytes" at the first byte of the filename
					bytes := (*[syscall.PathMax]byte)(unsafe.Pointer(&buf[offset+syscall.SizeofInotifyEvent]))
					// The filename is padded with NUL bytes. TrimRight() gets rid of those.
					event.Name += "/" + strings.TrimRight(string(bytes[0:nameLen]), "\000")
				}
				if !w.sendEvent(event) {
					return
				}
			}
			// Move to the next event in the buffer
			offset += syscall.SizeofInotifyEvent + nameLen
		}
	}
}

func (w *Watcher) sendEvent(event *Event) bool {
	select {
	case w.Event <- event:
		return true
	case <-w.done:
		return false
	}
}

func (w *Watcher) waitReadable() bool {
	for {
		select {
		case <-w.done:
			return false
		default:
		}
		pollFDs := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLIN}}
		_, err := unix.Poll(pollFDs, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			w.reportError(fmt.Errorf("inotify poll: %w", err))
			return false
		}
		if pollFDs[0].Revents&unix.POLLIN != 0 {
			return true
		}
		if pollFDs[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return false
		}
	}
}

func (w *Watcher) reportOverflow(mask uint32) bool {
	if mask&InQOverflow == 0 {
		return false
	}
	w.reportError(ErrEventOverflow)
	return true
}

func (w *Watcher) reportError(err error) {
	select {
	case w.Error <- err:
	default:
	}
}

// String formats the event e in the form
// "filename: 0xEventMask = IN_ACCESS|IN_ATTRIB_|..."
func (e *Event) String() string {
	var events string

	m := e.Mask
	for _, b := range eventBits {
		if m&b.Value == b.Value {
			m &^= b.Value
			events += "|" + b.Name
		}
	}

	if m != 0 {
		events += fmt.Sprintf("|%#x", m)
	}
	if len(events) > 0 {
		events = " == " + events[1:]
	}

	return fmt.Sprintf("%q: %#x%s", e.Name, e.Mask, events)
}

const (
	// Options for inotify_init() are not exported
	// IN_CLOEXEC    uint32 = syscall.IN_CLOEXEC
	// IN_NONBLOCK   uint32 = syscall.IN_NONBLOCK

	// Options for AddWatch

	// InDontFollow : Don't dereference pathname if it is a symbolic link
	InDontFollow uint32 = syscall.IN_DONT_FOLLOW
	// InOneshot : Monitor the filesystem object corresponding to pathname for one event, then remove from watch list
	InOneshot uint32 = syscall.IN_ONESHOT
	// InOnlydir : Watch pathname only if it is a directory
	InOnlydir uint32 = syscall.IN_ONLYDIR

	// The "IN_MASK_ADD" option is not exported, as AddWatch
	// adds it automatically, if there is already a watch for the given path
	// IN_MASK_ADD      uint32 = syscall.IN_MASK_ADD

	// Events

	// InAccess : File was accessed
	InAccess uint32 = syscall.IN_ACCESS
	// InAllEvents : Bit mask for all notify events
	InAllEvents uint32 = syscall.IN_ALL_EVENTS
	// InAttrib : Metadata changed
	InAttrib uint32 = syscall.IN_ATTRIB
	// InClose : Equates to IN_CLOSE_WRITE | IN_CLOSE_NOWRITE
	InClose uint32 = syscall.IN_CLOSE
	// InCloseNowrite : File or directory not opened for writing was closed
	InCloseNowrite uint32 = syscall.IN_CLOSE_NOWRITE
	// InCloseWrite : File opened for writing was closed
	InCloseWrite uint32 = syscall.IN_CLOSE_WRITE
	// InCreate : File/directory created in watched directory
	InCreate uint32 = syscall.IN_CREATE
	// InDelete : File/directory deleted from watched directory
	InDelete uint32 = syscall.IN_DELETE
	// InDeleteSelf : Watched file/directory was itself deleted
	InDeleteSelf uint32 = syscall.IN_DELETE_SELF
	// InModify : File was modified
	InModify uint32 = syscall.IN_MODIFY
	// InMove : Equates to IN_MOVED_FROM | IN_MOVED_TO
	InMove uint32 = syscall.IN_MOVE
	// InMovedFrom : Generated for the directory containing the old filename when a file is renamed
	InMovedFrom uint32 = syscall.IN_MOVED_FROM
	// InMovedTo : Generated for the directory containing the new filename when a file is renamed
	InMovedTo uint32 = syscall.IN_MOVED_TO
	// InMoveSelf : Watched file/directory was itself moved
	InMoveSelf uint32 = syscall.IN_MOVE_SELF
	// InOpen : File or directory was opened
	InOpen uint32 = syscall.IN_OPEN

	// Special events

	// InIsdir : Subject of this event is a directory
	InIsdir uint32 = syscall.IN_ISDIR
	// InIgnored : Watch was removed explicitly or automatically
	InIgnored uint32 = syscall.IN_IGNORED
	// InQOverflow : Event queue overflowed
	InQOverflow uint32 = syscall.IN_Q_OVERFLOW
	// InUnmount : Filesystem containing watched object was unmounted
	InUnmount uint32 = syscall.IN_UNMOUNT
)

var eventBits = []struct {
	Value uint32
	Name  string
}{
	{InAccess, "IN_ACCESS"},
	{InAttrib, "IN_ATTRIB"},
	{InClose, "IN_CLOSE"},
	{InCloseNowrite, "IN_CLOSE_NOWRITE"},
	{InCloseWrite, "IN_CLOSE_WRITE"},
	{InCreate, "IN_CREATE"},
	{InDelete, "IN_DELETE"},
	{InDeleteSelf, "IN_DELETE_SELF"},
	{InModify, "IN_MODIFY"},
	{InMove, "IN_MOVE"},
	{InMovedFrom, "IN_MOVED_FROM"},
	{InMovedTo, "IN_MOVED_TO"},
	{InMoveSelf, "IN_MOVE_SELF"},
	{InOpen, "IN_OPEN"},
	{InIsdir, "IN_ISDIR"},
	{InIgnored, "IN_IGNORED"},
	{InQOverflow, "IN_Q_OVERFLOW"},
	{InUnmount, "IN_UNMOUNT"},
}
