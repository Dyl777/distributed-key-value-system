package main

import (
	"container/list"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"time"
)

const heartbeatMaxFailures = 3

var (
	port = flag.Int("port", 8000, "HTTP port for this node")
	//the port used for inter-node communication
	initialNodesCSV = flag.String("nodes", "http://localhost:8000", "comma-separated list of node base URLs in cluster")
	//they are supposed to represent the other nodes which are to work together
	//through the http addresses provided, machines
	replicationFactor = flag.Int("rep", 3, "replication factor")
	//number of times a single piece of data can be replicated or redundantly stored across different nodes in the cluster
	W = flag.Int("W", 2, "write quorum (acks required)")
	//number of nodes that must acknowledge a write operation before it is considered successful
	R = flag.Int("R", 1, "read quorum (responses required)")
	//number of nodes that must respond to a read operation before it is considered successful
	heartbeatInterval = flag.Duration("hb", 2*time.Second, "heartbeat interval")
	//a heartbeat is the signals which are sent across the different machines (in the case here, http addresses/links to see
	//if they are still reachable or responsive)
	cacheSize = flag.Int("cache", 100, "LRU cache entries per node")
	//size of the cache, in this case, we will simulate caching by using an array
	// how long to wait for replicas to become available before failing a write
	writeWait = flag.Duration("write-wait", 3*time.Second, "max time to wait for replicas to become alive before failing a write")
)

func NewLRU(n int) *LRUCache {
	return &LRUCache{cap: n, ll: list.New(), mp: make(map[string]*list.Element)}
}

func (c *LRUCache) Get(k string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.mp[k]; ok {
		c.ll.MoveToFront(e)
		ce := e.Value.(*cacheEntry)
		return ce.val, true
	}
	return nil, false
}

func (c *LRUCache) Put(k string, v []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.mp[k]; ok {
		c.ll.MoveToFront(e)
		e.Value.(*cacheEntry).val = v
		return
	}
	ce := &cacheEntry{key: k, val: v, ts: time.Now().UnixNano()}
	e := c.ll.PushFront(ce)
	c.mp[k] = e
	if c.ll.Len() > c.cap {
		last := c.ll.Back()
		if last != nil {
			c.ll.Remove(last)
			delete(c.mp, last.Value.(*cacheEntry).key)
		}
	}
}

// ---------- Node methods ----------
func NewNode(baseURL string) *Node {
	return &Node{
		baseURL:    baseURL,
		store:      make(map[string]KVItem),
		nodeInfos:  make(map[string]*NodeInfo),
		cache:      NewLRU(*cacheSize),
		httpClient: &http.Client{Timeout: 2 * time.Second},
	}
}

func (n *Node) setCluster(nodes []string) {
	n.clusterMu.Lock()
	defer n.clusterMu.Unlock()
	n.nodes = sortAndUnique(nodes)
	now := time.Now()
	for _, u := range n.nodes {
		if info, ok := n.nodeInfos[u]; !ok {
			n.nodeInfos[u] = &NodeInfo{URL: u, Alive: true, LastSeen: now}
		} else {
			// reset status for re-joined nodes
			info.Alive = true
			info.LastSeen = now
			info.FailCount = 0
		}
	}
	// remove stale infos
	for u := range n.nodeInfos {
		found := false
		for _, v := range n.nodes {
			if u == v {
				found = true
				break
			}
		}
		if !found {
			delete(n.nodeInfos, u)
		}
	}
}

func (n *Node) getNodes() []string {
	n.clusterMu.RLock()
	defer n.clusterMu.RUnlock()
	out := make([]string, len(n.nodes))
	copy(out, n.nodes)
	return out
}

// consistent hashing: choose replicas clockwise from hashed key
func (n *Node) findReplicas(key string) []string {
	nodes := n.getNodes()
	if len(nodes) == 0 {
		return []string{n.baseURL}
	}
	h := int(hashKey(key))
	// create virtual ring by hashing node strings too (simple)
	// For simplicity just place nodes in sorted order and choose starting index by hash mod len
	start := h % len(nodes)
	rep := []string{}
	for i := 0; i < int(math.Min(float64(*replicationFactor), float64(len(nodes)))); i++ {
		idx := (start + i) % len(nodes)
		rep = append(rep, nodes[idx])
	}
	return rep
}

func (n *Node) isPrimaryFor(key string) bool {
	rep := n.findReplicas(key)
	if len(rep) == 0 {
		return false
	}
	return rep[0] == n.baseURL
}

// put local without network
func (n *Node) localPut(item KVItem) {
	n.mu.Lock()
	defer n.mu.Unlock()
	cur, ok := n.store[item.Key]
	if !ok || item.Timestamp >= cur.Timestamp {
		n.store[item.Key] = item
		n.cache.Put(item.Key, item.Value)
	}
}

// get local item
func (n *Node) localGet(key string) (KVItem, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	v, ok := n.store[key]
	return v, ok
}

// heartbeat ping endpoint
func (n *Node) handlePing(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(200)
	w.Write([]byte("pong"))
}

// list nodes
func (n *Node) handleNodes(w http.ResponseWriter, r *http.Request) {
	nodes := n.getNodes()
	b, _ := json.Marshal(nodes)
	w.WriteHeader(200)
	w.Write(b)
}

// join cluster: a new node contacts a seed -> it returns the cluster nodes and seed will add it
func (n *Node) handleJoin(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	newURL := string(body)
	if newURL == "" {
		http.Error(w, "empty body needs new node URL", 400)
		return
	}
	// add to our cluster
	nodes := n.getNodes()
	nodes = append(nodes, newURL)
	n.setCluster(nodes)
	log.Printf("[%s] node joined: %s", n.baseURL, newURL)
	// reply with cluster list
	resp := n.getNodes()
	b, _ := json.Marshal(resp)
	w.WriteHeader(200)
	w.Write(b)
}

// admin: return local keys (for debugging)
func (n *Node) handleLocalKeys(w http.ResponseWriter, r *http.Request) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	keys := make(map[string]int64)
	for k, v := range n.store {
		keys[k] = v.Timestamp
	}
	b, _ := json.Marshal(keys)
	w.WriteHeader(200)
	w.Write(b)
}

// ---------- housekeeping: heartbeat and rebalancing ----------
func (n *Node) heartbeatLoop() {
	t := time.NewTicker(*heartbeatInterval)
	for {
		<-t.C
		nodes := n.getNodes()
		for _, u := range nodes {
			if u == n.baseURL {
				continue
			}
			go func(target string) {
				url := fmt.Sprintf("%s/internal/ping", target)
				resp, err := n.httpClient.Get(url)
				n.clusterMu.Lock()
				defer n.clusterMu.Unlock()
				info, ok := n.nodeInfos[target]
				if !ok {
					info = &NodeInfo{URL: target}
					n.nodeInfos[target] = info
				}
				if err != nil || resp == nil || resp.StatusCode != 200 {
					// increment fail count; mark dead only after threshold
					info.FailCount++
					if info.FailCount >= heartbeatMaxFailures {
						info.Alive = false
					}
				} else {
					info.Alive = true
					info.LastSeen = time.Now()
					info.FailCount = 0
					resp.Body.Close()
				}
			}(u)
		}
		// if some nodes are dead -> rebuild nodes list excluding dead for ring
		n.clusterMu.Lock()
		aliveList := []string{}
		for _, u := range n.nodes {
			if inf, ok := n.nodeInfos[u]; ok && !inf.Alive {
				log.Printf("[%s] node %s appears DOWN", n.baseURL, u)
				continue
			}
			aliveList = append(aliveList, u)
		}
		if len(aliveList) > 0 {
			n.nodes = sortAndUnique(aliveList)
		}
		n.clusterMu.Unlock()
	}
}

// attempt to pull keys for which this node is now a replica (called on startup or membership change)
func (n *Node) rebalancePull() {
	nodes := n.getNodes()
	n.mu.RLock() // we will only lookup keys after pulling
	defer n.mu.RUnlock()
	// For each key in entire cluster? that is expensive. Instead, for demo: ask every other node for its keys and adopt those for which we are now a replica primary.
	for _, u := range nodes {
		if u == n.baseURL {
			continue
		}
		url := fmt.Sprintf("%s/internal/keys", u)
		resp, err := n.httpClient.Get(url)
		if err != nil || resp.StatusCode != 200 {
			continue
		}
		var remoteKeys map[string]int64
		if err := json.NewDecoder(resp.Body).Decode(&remoteKeys); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()
		for rk := range remoteKeys {
			rep := n.findReplicas(rk)
			shouldHave := false
			for _, r := range rep {
				if r == n.baseURL {
					shouldHave = true
					break
				}
			}
			if shouldHave {
				// pull the latest value from the replicas
				for _, r := range rep {
					if r == n.baseURL {
						continue
					}
					it, err := n.sendGet(r, rk)
					if err == nil {
						n.localPut(it)
						break
					}
				}
			}
		}
	}
}

// internal keys endpoint to help rebalance
func (n *Node) handleInternalKeys(w http.ResponseWriter, r *http.Request) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	keys := map[string]int64{}
	for k, v := range n.store {
		keys[k] = v.Timestamp
	}
	b, _ := json.Marshal(keys)
	w.WriteHeader(200)
	w.Write(b)
}

// ---------- start server ----------
func (n *Node) startServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/kv", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			n.handlePut(w, r)
			return
		}
		if r.Method == "GET" {
			n.handleGet(w, r)
			return
		}
		http.Error(w, "only GET/PUT", 405)
	})
	mux.HandleFunc("/internal/replicate", n.handleInternalReplicate)
	mux.HandleFunc("/internal/get", n.handleInternalGet)
	mux.HandleFunc("/internal/ping", n.handlePing)
	mux.HandleFunc("/internal/keys", n.handleInternalKeys)
	mux.HandleFunc("/join", n.handleJoin)
	mux.HandleFunc("/nodes", n.handleNodes)
	mux.HandleFunc("/local/keys", n.handleLocalKeys)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: mux,
	}
	log.Printf("node %s starting on %s", n.baseURL, srv.Addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Fatalf("server failed: %v", err)
		}
	}()
	// heartbeat loop
	go n.heartbeatLoop()
}
