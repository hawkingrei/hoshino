package heavykeeper

import (
	"container/list"
	"math/rand"
	"strconv"
	"testing"
)

func BenchmarkRetentionPolicies(b *testing.B) {
	const capacity = 10_000
	for _, trace := range []struct {
		name string
		keys []string
	}{
		{name: "uniform", keys: uniformTrace(100_000, 50_000)},
		{name: "zipfian", keys: zipfianTrace(100_000, 50_000)},
		{name: "scan", keys: scanTrace(100_000)},
		{name: "mixed-size", keys: mixedTrace(100_000)},
	} {
		b.Run(trace.name+"/heavykeeper", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				policy := NewHeavyKeeper(capacity, 1<<17, 4, 0.9, 1)
				for _, key := range trace.keys {
					policy.Add(key, 1)
				}
			}
		})
		b.Run(trace.name+"/lru", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				policy := newLRU(capacity)
				for _, key := range trace.keys {
					policy.Add(key)
				}
			}
		})
	}
}

func uniformTrace(length, keySpace int) []string {
	random := rand.New(rand.NewSource(1))
	trace := make([]string, length)
	for i := range trace {
		trace[i] = strconv.Itoa(random.Intn(keySpace))
	}
	return trace
}

func zipfianTrace(length, keySpace int) []string {
	random := rand.New(rand.NewSource(1))
	zipf := rand.NewZipf(random, 1.2, 1, uint64(keySpace-1))
	trace := make([]string, length)
	for i := range trace {
		trace[i] = strconv.FormatUint(zipf.Uint64(), 10)
	}
	return trace
}

func scanTrace(length int) []string {
	trace := make([]string, length)
	for i := range trace {
		trace[i] = strconv.Itoa(i)
	}
	return trace
}

func mixedTrace(length int) []string {
	trace := make([]string, length)
	for i := range trace {
		if i%5 == 0 {
			trace[i] = "large-hot-" + strconv.Itoa(i%100)
			continue
		}
		trace[i] = "small-scan-" + strconv.Itoa(i)
	}
	return trace
}

type lruSet struct {
	capacity int
	order    *list.List
	byKey    map[string]*list.Element
}

func newLRU(capacity int) *lruSet {
	return &lruSet{capacity: capacity, order: list.New(), byKey: make(map[string]*list.Element, capacity)}
}

func (l *lruSet) Add(key string) {
	if existing, ok := l.byKey[key]; ok {
		l.order.MoveToFront(existing)
		return
	}
	l.byKey[key] = l.order.PushFront(key)
	if l.order.Len() <= l.capacity {
		return
	}
	oldest := l.order.Back()
	delete(l.byKey, oldest.Value.(string))
	l.order.Remove(oldest)
}
