package eviction

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type ActivityLease struct {
	path string
}

func NewActivityLease(path string) (*ActivityLease, error) {
	if path == "" {
		return nil, errors.New("activity lock path must not be empty")
	}
	return &ActivityLease{path: path}, nil
}

func (l *ActivityLease) TryExclusive() (release func() error, acquired bool, err error) {
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o660)
	if err != nil {
		return nil, false, fmt.Errorf("open activity lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("acquire activity lock: %w", err)
	}
	return func() error {
		unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		closeErr := file.Close()
		return errors.Join(unlockErr, closeErr)
	}, true, nil
}
