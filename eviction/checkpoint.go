package eviction

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hawkingrei/hoshino/diskutil"
	"github.com/hawkingrei/hoshino/eviction/internal/heavykeeper"
)

const checkpointVersion = 1

type checkpoint struct {
	Version int                `json:"version"`
	SavedAt time.Time          `json:"saved_at"`
	Items   []heavykeeper.Item `json:"items"`
}

func loadCheckpoint(path string, maxItems uint32) ([]heavykeeper.Item, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open checkpoint: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<20))
	decoder.DisallowUnknownFields()
	var stored checkpoint
	if err := decoder.Decode(&stored); err != nil {
		return nil, false, fmt.Errorf("decode checkpoint: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, false, fmt.Errorf("checkpoint contains trailing data")
	}
	if stored.Version != checkpointVersion || len(stored.Items) > int(maxItems) {
		return nil, false, fmt.Errorf("unsupported or oversized checkpoint")
	}
	for _, item := range stored.Items {
		if _, err := diskutil.ParseKey(item.Key); err != nil || item.Count == 0 {
			return nil, false, fmt.Errorf("invalid checkpoint item %q", item.Key)
		}
	}
	return stored.Items, true, nil
}

func saveCheckpoint(path string, items []heavykeeper.Item, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create checkpoint directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".hot-set-*.tmp")
	if err != nil {
		return fmt.Errorf("create checkpoint temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o640); err != nil {
		temporary.Close()
		return err
	}
	stored := checkpoint{Version: checkpointVersion, SavedAt: now.UTC(), Items: items}
	if err := json.NewEncoder(temporary).Encode(stored); err != nil {
		temporary.Close()
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync checkpoint: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close checkpoint: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace checkpoint: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open checkpoint directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync checkpoint directory: %w", err)
	}
	return nil
}
