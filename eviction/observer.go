package eviction

import "github.com/hawkingrei/hoshino/diskutil"

type Observer interface {
	SetDisk(bytesFree, bytesUsed uint64)
	SetCacheBytes(bytes int64)
	SetReady(ready bool)
	RecordAccess(kind diskutil.EntryKind)
	RecordClose(kind diskutil.EntryKind)
	RecordProjected(bytes int64)
	RecordDeleted(kind diskutil.EntryKind, bytes int64)
	RecordSkippedActive()
	RecordFrozen(reason string)
	RecordReconciliation()
}

type noopObserver struct{}

func (noopObserver) SetDisk(uint64, uint64)                  {}
func (noopObserver) SetCacheBytes(int64)                     {}
func (noopObserver) SetReady(bool)                           {}
func (noopObserver) RecordAccess(diskutil.EntryKind)         {}
func (noopObserver) RecordClose(diskutil.EntryKind)          {}
func (noopObserver) RecordProjected(int64)                   {}
func (noopObserver) RecordDeleted(diskutil.EntryKind, int64) {}
func (noopObserver) RecordSkippedActive()                    {}
func (noopObserver) RecordFrozen(string)                     {}
func (noopObserver) RecordReconciliation()                   {}
