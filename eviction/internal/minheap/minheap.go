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
	if existing, ok := h.byKey[val.Key]; ok {
		h.Fix(existing.index, val.Count)
		return nil
	}
	if h.K == 0 {
		return val
	}
	if h.K > uint32(len(h.Nodes)) {
		heap.Push(&h.Nodes, val)
		h.byKey[val.Key] = val
	} else if val.Count > h.Nodes[0].Count {
		expelled := heap.Pop(&h.Nodes)
		delete(h.byKey, expelled.(*Node).Key)
		heap.Push(&h.Nodes, val)
		h.byKey[val.Key] = val
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
