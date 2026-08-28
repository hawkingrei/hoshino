package minheap

import (
	"strconv"
	"testing"
)

func TestHeapMaintainsKeyIndices(t *testing.T) {
	h := NewHeap(3)
	h.Add(&Node{Key: "a", Count: 3})
	h.Add(&Node{Key: "b", Count: 1})
	h.Add(&Node{Key: "c", Count: 2})
	if h.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", h.Len())
	}
	if !h.Contains("b") || h.Contains("missing") {
		t.Fatal("Contains() returned an unexpected membership result")
	}
	assertIndexInvariant(t, h)

	bIndex, ok := h.Find("b")
	if !ok {
		t.Fatal("Find(b) did not find an existing key")
	}
	h.Fix(bIndex, 4)
	assertIndexInvariant(t, h)

	_ = h.Sorted()
	assertIndexInvariant(t, h)

	expelled := h.Add(&Node{Key: "d", Count: 5})
	if expelled == nil || expelled.Key != "c" {
		t.Fatalf("Add(d) expelled %v, want c", expelled)
	}
	if _, ok := h.Find("c"); ok {
		t.Fatal("Find(c) found an expelled key")
	}
	assertIndexInvariant(t, h)

	popped := h.Pop()
	if popped.Key != "a" {
		t.Fatalf("Pop() = %q, want a", popped.Key)
	}
	if _, ok := h.Find("a"); ok {
		t.Fatal("Find(a) found a popped key")
	}
	assertIndexInvariant(t, h)
}

func TestHeapUpsertDoesNotAllocateRejectedCandidate(t *testing.T) {
	h := NewHeap(1)
	h.Upsert("hot", 1)

	allocations := testing.AllocsPerRun(1000, func() {
		h.Upsert("cold", 1)
	})
	if allocations != 0 {
		t.Fatalf("Upsert() allocations = %v, want 0", allocations)
	}
	if h.Contains("cold") {
		t.Fatal("rejected candidate was added to the heap")
	}
}

func TestHeapAddUpdatesExistingKey(t *testing.T) {
	h := NewHeap(2)
	h.Add(&Node{Key: "a", Count: 1})
	h.Add(&Node{Key: "a", Count: 5})

	if len(h.Nodes) != 1 {
		t.Fatalf("heap length = %d, want 1", len(h.Nodes))
	}
	index, ok := h.Find("a")
	if !ok || h.Nodes[index].Count != 5 {
		t.Fatalf("updated node = %v, want count 5", h.Nodes)
	}
	assertIndexInvariant(t, h)
}

func assertIndexInvariant(t *testing.T, h *Heap) {
	t.Helper()
	if len(h.byKey) != len(h.Nodes) {
		t.Fatalf("index size = %d, heap size = %d", len(h.byKey), len(h.Nodes))
	}
	for index, node := range h.Nodes {
		if node.index != index {
			t.Fatalf("node %q index = %d, want %d", node.Key, node.index, index)
		}
		indexed, ok := h.byKey[node.Key]
		if !ok || indexed != node {
			t.Fatalf("index entry for %q = %p, want %p", node.Key, indexed, node)
		}
	}
}

func BenchmarkFindMillionEntries(b *testing.B) {
	const size = 1_000_000
	h := NewHeap(size)
	keys := make([]string, size)
	for i := 0; i < size; i++ {
		keys[i] = strconv.Itoa(i)
		h.Add(&Node{Key: keys[i], Count: uint32(i + 1)})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Find(keys[i%size])
	}
}
