package diskutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type EntryKind string

const (
	ActionCache EntryKind = "ac"
	ContentCAS  EntryKind = "cas"
)

var (
	ErrInvalidCacheEntry = errors.New("invalid Bazel disk-cache entry")
	ErrEntryChanged      = errors.New("cache entry changed since selection")
)

// EntryInfo is an immutable snapshot used to guard direct eviction.
type EntryInfo struct {
	Key     string
	Path    string
	Kind    EntryKind
	Size    int64
	ModTime time.Time
	Device  uint64
	Inode   uint64
}

type Cache struct {
	diskRoot   string
	rootDevice uint64
	rootInode  uint64
}

func NewCache(diskRoot string) (*Cache, error) {
	root, err := filepath.Abs(filepath.Clean(diskRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve cache root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve cache root symlinks: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect cache root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("cache root must be a real directory: %s", root)
	}
	device, inode, ok := fileIdentity(info)
	if !ok {
		return nil, fmt.Errorf("cache root identity is unavailable: %s", root)
	}
	return &Cache{diskRoot: root, rootDevice: device, rootInode: inode}, nil
}

func (c *Cache) DiskRoot() string {
	return c.diskRoot
}

// ParseKey accepts only completed SHA-256 entries from Bazel's native cache.
// Temporary files, GC state, directories, and unknown digest layouts are rejected.
func ParseKey(key string) (EntryKind, error) {
	clean := filepath.ToSlash(filepath.Clean(key))
	if clean != key || filepath.IsAbs(key) {
		return "", fmt.Errorf("%w: non-canonical key %q", ErrInvalidCacheEntry, key)
	}
	parts := strings.Split(clean, "/")
	if len(parts) != 3 {
		return "", fmt.Errorf("%w: unexpected path shape %q", ErrInvalidCacheEntry, key)
	}
	kind := EntryKind(parts[0])
	if kind != ActionCache && kind != ContentCAS {
		return "", fmt.Errorf("%w: unsupported store %q", ErrInvalidCacheEntry, parts[0])
	}
	if len(parts[1]) != 2 || len(parts[2]) != 64 || parts[1] != parts[2][:2] {
		return "", fmt.Errorf("%w: invalid SHA-256 path %q", ErrInvalidCacheEntry, key)
	}
	if !isLowerHex(parts[1]) || !isLowerHex(parts[2]) {
		return "", fmt.Errorf("%w: digest must be lowercase hexadecimal", ErrInvalidCacheEntry)
	}
	return kind, nil
}

func isLowerHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (c *Cache) KeyForPath(path string) (string, EntryKind, error) {
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", fmt.Errorf("resolve cache path: %w", err)
	}
	rel, err := filepath.Rel(c.diskRoot, absPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve cache-relative path: %w", err)
	}
	key := filepath.ToSlash(rel)
	kind, err := ParseKey(key)
	if err != nil {
		return "", "", err
	}
	return key, kind, nil
}

func (c *Cache) Snapshot(key string) (EntryInfo, error) {
	kind, err := ParseKey(key)
	if err != nil {
		return EntryInfo{}, err
	}
	path := filepath.Join(c.diskRoot, filepath.FromSlash(key))
	info, err := os.Lstat(path)
	if err != nil {
		return EntryInfo{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return EntryInfo{}, fmt.Errorf("%w: entry is not a regular file", ErrInvalidCacheEntry)
	}
	device, inode, ok := fileIdentity(info)
	if !ok {
		return EntryInfo{}, fmt.Errorf("%w: file identity is unavailable", ErrInvalidCacheEntry)
	}
	return EntryInfo{
		Key:     key,
		Path:    path,
		Kind:    kind,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		Device:  device,
		Inode:   inode,
	}, nil
}

func (c *Cache) GetEntries() ([]EntryInfo, error) {
	entries := make([]EntryInfo, 0)
	for _, kind := range []EntryKind{ActionCache, ContentCAS} {
		storeRoot := filepath.Join(c.diskRoot, string(kind))
		err := filepath.WalkDir(storeRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, fs.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			key, _, err := c.KeyForPath(path)
			if err != nil {
				return nil
			}
			snapshot, err := c.Snapshot(key)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrInvalidCacheEntry) {
					return nil
				}
				return err
			}
			entries = append(entries, snapshot)
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("scan %s cache: %w", kind, err)
		}
	}
	return entries, nil
}

func (c *Cache) Delete(snapshot EntryInfo) error {
	if snapshot.Key == "" {
		return fmt.Errorf("%w: empty snapshot key", ErrInvalidCacheEntry)
	}
	return c.deleteSnapshot(snapshot)
}
