package heavykeeper

import (
	"math"
	"math/rand"
	"sort"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTopkList(t *testing.T) {
	// zipfan distribution
	zipf := rand.NewZipf(rand.New(rand.NewSource(1)), 3, 2, 1000)
	topk := NewHeavyKeeper(10, 10000, 5, 0.925, 0)
	dataMap := make(map[string]int)
	for i := 0; i < 10000; i++ {
		key := strconv.FormatUint(zipf.Uint64(), 10)
		dataMap[key] = dataMap[key] + 1
		topk.Add(key, 1)
	}
	var rate float64
	for _, node := range topk.List() {
		rate += math.Abs(float64(node.Count)-float64(dataMap[node.Key])) / float64(dataMap[node.Key])
		t.Logf("item %s, count %d, expect %d", node.Key, node.Count, dataMap[node.Key])
	}
	t.Logf("err rate avg:%f", rate)
	expected := make([]Item, 0, len(dataMap))
	for key, count := range dataMap {
		expected = append(expected, Item{Key: key, Count: uint32(count)})
	}
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].Count != expected[j].Count {
			return expected[i].Count > expected[j].Count
		}
		return expected[i].Key < expected[j].Key
	})
	for i, node := range topk.List() {
		assert.Equal(t, expected[i].Key, node.Key)
		t.Logf("%s: %d", node.Key, node.Count)
	}
}

func BenchmarkAdd(b *testing.B) {
	zipf := rand.NewZipf(rand.New(rand.NewSource(1)), 2, 2, 1000)
	var data []string = make([]string, 1000)
	for i := 0; i < 1000; i++ {
		data[i] = strconv.FormatUint(zipf.Uint64(), 10)
	}
	topk := NewHeavyKeeper(10, 1000, 5, 0.9, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		topk.Add(data[i%1000], 1)
	}
}

func TestReset(t *testing.T) {
	topk := NewHeavyKeeper(2, 32, 2, 0.9, 1).(*HeavyKeeper)
	topk.Add("first", 10)
	topk.Add("second", 5)
	if topk.Len() != 2 {
		t.Fatalf("Len() before reset = %d, want 2", topk.Len())
	}
	if !topk.Contains("first") || !topk.Contains("second") {
		t.Fatal("Contains() did not find tracked keys before reset")
	}
	if len(topk.List()) == 0 {
		t.Fatal("top-k should contain entries before reset")
	}

	topk.Reset()

	if got := topk.List(); len(got) != 0 {
		t.Fatalf("List() after reset = %v, want empty", got)
	}
	if got := topk.Total(); got != 0 {
		t.Fatalf("Total() after reset = %d, want 0", got)
	}
	if topk.Len() != 0 || topk.Contains("first") || topk.Contains("second") {
		t.Fatal("top-k membership was not cleared by reset")
	}
	topk.Add("rebuilt", 1)
	if got := topk.List(); len(got) != 1 || got[0].Key != "rebuilt" {
		t.Fatalf("List() after rebuild = %v, want rebuilt entry", got)
	}
}

func TestConcurrentPolicyAccess(t *testing.T) {
	topk := NewHeavyKeeper(128, 1024, 4, 0.9, 1)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for i := 0; i < 1000; i++ {
				key := strconv.Itoa((worker * 1000) + i)
				topk.Add(key, 1)
				topk.Contains(key)
				if i%100 == 0 {
					topk.List()
				}
			}
		}(worker)
	}
	workers.Wait()
	topk.Fading()
	topk.Remove("1")
	topk.Restore(topk.List())
	if topk.Len() > 128 {
		t.Fatalf("Len() = %d, want at most 128", topk.Len())
	}
}
