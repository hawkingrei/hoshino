//go:build linux

package diskutil

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

func (c *Cache) deleteSnapshot(snapshot EntryInfo) error {
	kind, err := ParseKey(snapshot.Key)
	if err != nil || kind != snapshot.Kind {
		return fmt.Errorf("%w: snapshot key changed", ErrInvalidCacheEntry)
	}
	parts := strings.Split(snapshot.Key, "/")
	rootFD, err := unix.Open(c.diskRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open cache root: %w", err)
	}
	defer unix.Close(rootFD)
	var rootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil {
		return fmt.Errorf("inspect cache root: %w", err)
	}
	if uint64(rootStat.Dev) != c.rootDevice || rootStat.Ino != c.rootInode {
		return fmt.Errorf("%w: cache root was replaced", ErrEntryChanged)
	}
	storeFD, err := unix.Openat(rootFD, parts[0], unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open cache store: %w", err)
	}
	defer unix.Close(storeFD)
	shardFD, err := unix.Openat(storeFD, parts[1], unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open cache shard: %w", err)
	}
	defer unix.Close(shardFD)

	var stat unix.Stat_t
	if err := unix.Fstatat(shardFD, parts[2], &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || uint64(stat.Dev) != snapshot.Device || stat.Ino != snapshot.Inode || stat.Size != snapshot.Size || stat.Mtim.Nano() != snapshot.ModTime.UnixNano() {
		return ErrEntryChanged
	}
	if err := unix.Unlinkat(shardFD, parts[2], 0); err != nil {
		return fmt.Errorf("unlink cache entry: %w", err)
	}
	return nil
}
