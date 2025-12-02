package main

import (
	"container/list"
	"net/http"
	"sync"
	"time"
)

type KVItem struct {
	Key       string
	Value     []byte
	Timestamp int64 // simple last-write-wins
}

type NodeInfo struct {
	URL      string
	Alive    bool
	LastSeen time.Time
	// number of consecutive failed heartbeats; only mark node down after exceeding threshold
	FailCount int
}

type Node struct {
	baseURL string

	// storage
	mu    sync.RWMutex
	store map[string]KVItem

	// ring & cluster membership
	clusterMu sync.RWMutex
	nodes     []string // sorted set of node URLs (the ring positions)
	nodeInfos map[string]*NodeInfo

	// cache
	cache *LRUCache

	// internal
	httpClient *http.Client
}

// ---------- LRU cache ----------
type cacheEntry struct {
	key string
	val []byte
	ts  int64
}

type LRUCache struct {
	cap int
	mu  sync.Mutex
	ll  *list.List
	mp  map[string]*list.Element
}
