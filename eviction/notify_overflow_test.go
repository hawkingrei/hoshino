package eviction

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/hawkingrei/hoshino/eviction/internal/heavykeeper"
)

func TestEnqueueAccessInvalidatesFullQueue(t *testing.T) {
	notify := &Notify{}
	accesses := make(chan accessEvent, 1)
	accesses <- accessEvent{key: "queued"}

	if notify.enqueueAccess(accesses, accessEvent{key: "overflow"}) {
		t.Fatal("enqueueAccess() succeeded for a full queue")
	}
	if !notify.eventStateInvalid.Load() {
		t.Fatal("full queue did not invalidate hot-key state")
	}
}

func TestRunWorkerResetsAndRebuildsAfterOverflow(t *testing.T) {
	topk := newRecoveringTopk()
	notify := &Notify{heavykeeper: topk}
	accesses := make(chan accessEvent, 2)
	accesses <- accessEvent{key: "stale", increment: 1}
	notify.eventStateInvalid.Store(true)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		notify.runWorker(accesses)
	}()

	deadline := time.After(time.Second)
	for notify.eventStateInvalid.Load() {
		select {
		case <-deadline:
			t.Fatal("worker did not reset invalid state")
		default:
			runtime.Gosched()
		}
	}

	accesses <- accessEvent{key: "rebuilt", increment: 1}
	close(accesses)
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after the access queue closed")
	}

	added, resets := topk.snapshot()
	if resets != 1 {
		t.Fatalf("Reset() calls = %d, want 1", resets)
	}
	if len(added) != 1 || added[0] != "rebuilt" {
		t.Fatalf("added keys = %v, want [rebuilt]", added)
	}
}

type recoveringTopk struct {
	mu       sync.Mutex
	added    []string
	resets   int
	expelled chan heavykeeper.Item
}

func newRecoveringTopk() *recoveringTopk {
	return &recoveringTopk{expelled: make(chan heavykeeper.Item)}
}

func (t *recoveringTopk) Add(key string, _ uint32) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.added = append(t.added, key)
	return "", true
}

func (t *recoveringTopk) List() []heavykeeper.Item {
	return nil
}

func (t *recoveringTopk) Expelled() <-chan heavykeeper.Item {
	return t.expelled
}

func (t *recoveringTopk) Fading() {}

func (t *recoveringTopk) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resets++
	t.added = nil
}

func (t *recoveringTopk) snapshot() ([]string, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.added...), t.resets
}
