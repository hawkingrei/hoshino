package eviction

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestActivityLeaseSkipsWhileSharedHolderIsActive(t *testing.T) {
	path := t.TempDir() + "/activity.lock"
	shared, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o660)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	if err := unix.Flock(int(shared.Fd()), unix.LOCK_SH); err != nil {
		t.Fatal(err)
	}

	lease, err := NewActivityLease(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, acquired, err := lease.TryExclusive(); err != nil || acquired {
		t.Fatalf("TryExclusive() = acquired %v, error %v; want busy", acquired, err)
	}
	if err := unix.Flock(int(shared.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	release, acquired, err := lease.TryExclusive()
	if err != nil || !acquired {
		t.Fatalf("TryExclusive() = acquired %v, error %v; want acquired", acquired, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
