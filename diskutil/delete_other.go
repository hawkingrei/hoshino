//go:build !linux

package diskutil

import "fmt"

func (c *Cache) deleteSnapshot(snapshot EntryInfo) error {
	return fmt.Errorf("secure cache deletion is supported only on Linux: %s", snapshot.Key)
}
