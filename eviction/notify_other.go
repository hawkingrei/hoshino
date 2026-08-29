//go:build !linux

package eviction

import (
	"context"
	"errors"
)

type Notify struct{}

func New(Config) (*Notify, error) {
	return nil, errors.New("Hoshino native Bazel disk-cache management requires Linux")
}

func (*Notify) Ready() bool {
	return false
}

func (*Notify) Run(context.Context) error {
	return errors.New("Hoshino native Bazel disk-cache management requires Linux")
}
