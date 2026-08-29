package eviction

type Config struct {
	CacheDir       string
	ActivityLock   string
	CheckpointFile string
	Policy         EvictionPolicy
	Observer       Observer
}
