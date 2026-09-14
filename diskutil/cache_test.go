package diskutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseKey(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, key := range []string{"ac/aa/" + digest, "cas/aa/" + digest} {
		if _, err := ParseKey(key); err != nil {
			t.Fatalf("ParseKey(%q) error = %v", key, err)
		}
	}
	for _, key := range []string{
		"tmp/aa/" + digest,
		"cas/bb/" + digest,
		"cas/aa/../" + digest,
		"cas/aa/" + strings.ToUpper(digest),
		"cas/aa/short",
		"/cas/aa/" + digest,
	} {
		if _, err := ParseKey(key); !errors.Is(err, ErrInvalidCacheEntry) {
			t.Fatalf("ParseKey(%q) error = %v, want ErrInvalidCacheEntry", key, err)
		}
	}
}

func TestGetEntriesExcludesUnknownAndSymlinkPaths(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("a", 64)
	valid := filepath.Join(root, "cas", "aa", digest)
	writeTestFile(t, valid)
	writeTestFile(t, filepath.Join(root, "tmp", "write-in-progress"))
	writeTestFile(t, filepath.Join(root, "cas", "aa", "invalid"))
	if err := os.Symlink(valid, filepath.Join(root, "cas", "aa", strings.Repeat("b", 64))); err != nil {
		t.Fatal(err)
	}

	cache, err := NewCache(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := cache.GetEntries()
	if err != nil {
		t.Fatal(err)
	}
	resolvedValid, err := filepath.EvalSymlinks(valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != resolvedValid {
		t.Fatalf("GetEntries() = %#v, want only %q", entries, resolvedValid)
	}
}

func TestGetEntriesExcludesRepositoryCaches(t *testing.T) {
	root := t.TempDir()
	digest := strings.Repeat("a", 64)
	want := map[string]bool{
		"ac/aa/" + digest:  true,
		"cas/aa/" + digest: true,
	}
	for key := range want {
		writeTestFile(t, filepath.Join(root, filepath.FromSlash(key)))
	}
	repository := filepath.Join(root, "contents", digest, "00000000-0000-4000-8000-000000000001")
	for _, path := range []string{
		filepath.Join(repository, "MODULE.bazel"),
		filepath.Join(repository, "ac", "aa", digest),
		filepath.Join(repository, "cas", "aa", digest),
		filepath.Join(root, "content_addressable", "sha256", digest, "file"),
	} {
		writeTestFile(t, path)
	}
	if err := os.Symlink("missing-uuid", filepath.Join(root, "contents", digest, "dangling")); err != nil {
		t.Fatal(err)
	}

	cache, err := NewCache(root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := cache.GetEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		t.Fatalf("GetEntries() = %#v, want only native disk-cache entries", entries)
	}
	for _, entry := range entries {
		if !want[entry.Key] {
			t.Fatalf("unexpected cache entry: %q", entry.Key)
		}
		delete(want, entry.Key)
	}
}

func TestDeleteRejectsReplacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("secure deletion is Linux-only")
	}
	root := t.TempDir()
	digest := strings.Repeat("c", 64)
	key := "ac/cc/" + digest
	path := filepath.Join(root, filepath.FromSlash(key))
	writeTestFile(t, path)
	cache, err := NewCache(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := cache.Snapshot(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path)
	if err := cache.Delete(snapshot); !errors.Is(err, ErrEntryChanged) {
		t.Fatalf("Delete() error = %v, want ErrEntryChanged", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replacement was deleted: %v", err)
	}
}

func TestDeleteRejectsReplacedCacheRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("secure deletion is Linux-only")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "cache")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("d", 64)
	key := "cas/dd/" + digest
	path := filepath.Join(root, filepath.FromSlash(key))
	writeTestFile(t, path)
	cache, err := NewCache(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := cache.Snapshot(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	replacementPath := filepath.Join(root, filepath.FromSlash(key))
	writeTestFile(t, replacementPath)
	if err := cache.Delete(snapshot); !errors.Is(err, ErrEntryChanged) {
		t.Fatalf("Delete() error = %v, want ErrEntryChanged", err)
	}
	if _, err := os.Stat(replacementPath); err != nil {
		t.Fatalf("entry under replacement root was deleted: %v", err)
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
}
