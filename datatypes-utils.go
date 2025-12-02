package main

/*
Copyright (C) 2025 AMBE

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.

The comments below explain the core data model and concurrency
considerations used throughout the codebase. They are intentionally
verbose to help readers understand why locks, channels, and other
primitives (I wanted to experiment with) are used at particular locations.
*/

import (
	"container/list"
	"net/http"
	"sync"
	"time"
)

/*
KVItem represents the stored value and its metadata.

Fields:
  - Key: the user-visible key. Kept as a string for simplicity.
  - Value: the raw bytes stored for the key.
  - Timestamp: a monotonic timestamp (nanoseconds since epoch) used
    for last-write-wins conflict resolution across replicas. Using a
    simple integer timestamp avoids depending on external consensus
    or vector clocks for this demo.

Rationale: a small immutable struct makes it easy to serialize via
`encoding/gob` for efficient node-to-node replication.
*/
type KVItem struct {
	Key       string
	Value     []byte
	Timestamp int64 // simple last-write-wins
}

/*
NodeInfo holds per-node liveness/metadata used by the heartbeat
and membership code.

Fields and concurrency notes:
  - URL: the base URL of the remote node (http://host:port).
  - Alive: a boolean flag maintained by heartbeat checks. It is used
    as a quick indicator of whether we should include the node when
    selecting replicas or making network calls.
  - LastSeen: the last time we observed a successful response from
    that node. Useful for debugging and potential eviction policies.
  - FailCount: a small integer counting consecutive failed
    heartbeats. This code uses a conservative approach (require N
    consecutive failures before marking a node down) to avoid
    flapping during brief network hiccups.

Why track FailCount:- Immediately marking a node down on the first
failed ping can cause transient loss of quorum and unnecessary
rebalance activity. Counting failures allows the cluster to be
resilient to momentary network noise.
*/
type NodeInfo struct {
	URL      string
	Alive    bool
	LastSeen time.Time
	// number of consecutive failed heartbeats; only mark node down after exceeding threshold
	FailCount int
}

/*
Node encapsulates the runtime state of a single process/node in the
cluster.

Concurrency and fields explanation:
- baseURL: the node's address. Immutable after startup.

Storage section:
  - mu (sync.RWMutex): guards access to `store` and is used to protect
    the in-memory key/value map. We use a RWMutex to allow concurrent
    readers in `handleGet` while writes take the exclusive lock.
  - store: the authoritative map of keys to `KVItem` stored locally.

Ring & membership:
  - clusterMu (sync.RWMutex): protects `nodes` and `nodeInfos` which
    can be concurrently read by request handlers and concurrently
    modified by heartbeat/join logic. Using a separate lock for
    cluster metadata keeps it independent from `mu` which guards
    the actual KV store.
  - nodes: a sorted list representing the current ring positions. It
    is used by `findReplicas` to deterministically pick replicas for
    a given key.
  - nodeInfos: map of URL->*NodeInfo holding per-node liveness
    details. The pointers allow us to update `NodeInfo` in-place when
    a heartbeat updates the status.

Cache + internal:
  - cache: a pointer to an `LRUCache` used to speed up reads.
    The LRU cache uses its own internal mutex and is intentionally a
    separate component to avoid complicating store locking.
  - httpClient: shared HTTP client with a timeout used for
    node-to-node calls; it is safe for concurrent use.

NB:
  - Separating locks (store vs cluster) avoids unnecessary contention
    between read/write requests and membership maintenance.
  - Many operations rely on snapshot-style reads of `nodes` and
    `nodeInfos`. Copying slices or using small critical sections keeps
    operations cheap and safe.
*/
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


/*
The LRUCache is a space-limited local cache used to speed up reads.

Why LRU? A Least-Recently-Used policy fits workloads where recent
reads are likely to be read again (temporal locality). It reduces
latency and offloads frequent reads from the heavier path that may
involve contacting remote replicas.

Implementation notes and trade-offs:
- We store values as raw []byte. The cache stores pointers to list
	elements and a map for O(1) lookups and O(1) eviction.
- The implementation is intentionally simple: one mutex guards the
	entire cache. This simplifies reasoning and is sufficient for
	modest cache sizes (default 100). If contention becomes a
	bottleneck, the cache could be sharded.
- The name `LRUCache` is explicit: it describes both the policy
	(LRU) and the purpose (local in-memory cache). The `NewLRU`
	constructor emphasizes the intention to size the structure at
	creation based on `cacheSize`.
*/
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
