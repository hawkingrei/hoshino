package heavykeeper

// Item is topk item.
type Item struct {
	Key   string
	Count uint32
}

// Topk algorithm interface.
type Topk interface {
	// Add item and return if item is in the topk.
	Add(item string, incr uint32) (string, bool)
	// List all topk items.
	List() []Item
	// Len returns the number of tracked top-k items.
	Len() int
	// Contains reports whether an item is currently tracked in the top-k set.
	Contains(item string) bool
	// Expelled watch at the expelled items.
	Expelled() <-chan Item
	Fading()
	// Reset discards all observations while preserving the expelled channel.
	Reset()
}
