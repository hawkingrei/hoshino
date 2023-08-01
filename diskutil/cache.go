package diskutil

import (
	"github.com/sirupsen/logrus"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cache implements disk backed cache storage
type Cache struct {
	diskRoot string
	logger   *logrus.Entry
}

// NewCache returns a new Cache given the root directory that should be used
// on disk for cache storage
func NewCache(diskRoot string) *Cache {
	return &Cache{
		diskRoot: strings.TrimSuffix(diskRoot, string(os.PathListSeparator)),
	}
}

// KeyToPath converts a cache entry key to a path on disk
func (c *Cache) KeyToPath(key string) string {
	return filepath.Join(c.diskRoot, key)
}

// PathToKey converts a path on disk to a key, assuming the path is actually
// under DiskRoot() ...
func (c *Cache) PathToKey(key string) string {
	return strings.TrimPrefix(key, c.diskRoot+string(os.PathSeparator))
}

// DiskRoot returns the root directory containing all on-disk cache entries
func (c *Cache) DiskRoot() string {
	return c.diskRoot
}

// EntryInfo are returned when getting entries from the cache
type EntryInfo struct {
	Path       string
	LastAccess time.Time
}

// GetEntries walks the cache dir and returns all paths that exist
// In the future this *may* be made smarter
func (c *Cache) GetEntries() []EntryInfo {
	entries := []EntryInfo{}
	// note we swallow errors because we just need to know what keys exist
	// some keys missing is OK since this is used for eviction, but not returning
	// any of the keys due to some error is NOT
	_ = filepath.Walk(c.diskRoot, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			logrus.WithError(err).Error("error getting some entries")
			return nil
		}
		if !f.IsDir() {
			atime := GetATime(path, time.Now())
			entries = append(entries, EntryInfo{
				Path:       path,
				LastAccess: atime,
			})
		}
		return nil
	})
	return entries
}

// Delete deletes the file at key
func (c *Cache) Delete(key string) error {
	return os.Remove(c.KeyToPath(key))
}
