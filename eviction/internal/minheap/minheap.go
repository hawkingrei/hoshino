package minheap

import (
	"container/heap"
	"sort"
)

type Heap struct {
	Nodes Nodes
	K     uint32
	byKey map[string]*Node
}

func NewHeap(k uint32) *Heap {
	h := Nodes{}
	heap.Init(&h)
	return &Heap{Nodes: h, K: k, byKey: make(map[string]*Node)}
}

func (h *Heap) Add(val *Node) *Node {
	return h.upsert(val.Key, val.Count, val)
}

// Upsert updates an existing key without allocating rejected candidates.
func (h *Heap) Upsert(key string, count uint32) *Node {
	return h.upsert(key, count, nil)
}

func (h *Heap) upsert(key string, count uint32, candidate *Node) *Node {
	if existing, ok := h.byKey[key]; ok {
		h.Fix(existing.index, count)
		return nil
	}
	if h.K == 0 {
		if candidate == nil {
			candidate = &Node{Key: key, Count: count}
		}
		return candidate
	}
	if h.K > uint32(len(h.Nodes)) {
		if candidate == nil {
			candidate = &Node{Key: key, Count: count}
		}
		heap.Push(&h.Nodes, candidate)
		h.byKey[key] = candidate
	} else if count > h.Nodes[0].Count {
		expelled := heap.Pop(&h.Nodes)
		delete(h.byKey, expelled.(*Node).Key)
		if candidate == nil {
			candidate = &Node{Key: key, Count: count}
		}
		heap.Push(&h.Nodes, candidate)
		h.byKey[key] = candidate
		node := expelled.(*Node)
		return node
	}
	return nil
}

func (h *Heap) Pop() *Node {
	expelled := heap.Pop(&h.Nodes)
	node := expelled.(*Node)
	delete(h.byKey, node.Key)
	return node
}

func (h *Heap) Fix(idx int, count uint32) {
	h.Nodes[idx].Count = count
	heap.Fix(&h.Nodes, idx)
}

func (h *Heap) Min() uint32 {
	if len(h.Nodes) == 0 {
		return 0
	}
	return h.Nodes[0].Count
}

func (h *Heap) Len() int {
	return len(h.Nodes)
}

func (h *Heap) Contains(key string) bool {
	_, ok := h.byKey[key]
	return ok
}

func (h *Heap) Remove(key string) bool {
	node, ok := h.byKey[key]
	if !ok {
		return false
	}
	heap.Remove(&h.Nodes, node.index)
	delete(h.byKey, key)
	return true
}

func (h *Heap) ScaleDown() {
	for _, node := range h.Nodes {
		node.Count >>= 1
	}
	heap.Init(&h.Nodes)
	for len(h.Nodes) > 0 && h.Nodes[0].Count == 0 {
		h.Pop()
	}
}

func (h *Heap) Find(key string) (int, bool) {
	node, ok := h.byKey[key]
	if !ok {
		return 0, false
	}
	return node.index, true
}

func (h *Heap) Sorted() Nodes {
	nodes := make(Nodes, len(h.Nodes))
	for i, node := range h.Nodes {
		copy := *node
		copy.index = i
		nodes[i] = &copy
	}
	sort.Sort(sort.Reverse(Nodes(nodes)))
	return nodes
}

type Nodes []*Node

type Node struct {
	Key   string
	Count uint32
	index int
}

func (n Nodes) Len() int {
	return len(n)
}

func (n Nodes) Less(i, j int) bool {
	return (n[i].Count < n[j].Count) || (n[i].Count == n[j].Count && n[i].Key > n[j].Key)
}

func (n Nodes) Swap(i, j int) {
	n[i], n[j] = n[j], n[i]
	n[i].index = i
	n[j].index = j
}

func (n *Nodes) Push(val interface{}) {
	node := val.(*Node)
	node.index = len(*n)
	*n = append(*n, node)
}

func (n *Nodes) Pop() interface{} {
	old := *n
	last := len(old) - 1
	val := old[last]
	old[last] = nil
	*n = old[:last]
	val.index = -1
	return val
}
