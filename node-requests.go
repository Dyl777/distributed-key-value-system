package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// ---------- Handlers ----------
func (n *Node) handlePut(w http.ResponseWriter, r *http.Request) {
	// user-facing PUT: /kv?key=... body raw bytes
	k := r.URL.Query().Get("key")
	if k == "" {
		http.Error(w, "missing key", 400)
		return
	}
	body, _ := io.ReadAll(r.Body)
	ts := time.Now().UnixNano()
	item := KVItem{Key: k, Value: body, Timestamp: ts}
	replicas := n.findReplicas(k)
	if len(replicas) == 0 {
		n.localPut(item)
		w.WriteHeader(200)
		w.Write([]byte("ok-local"))
		return
	}

	// Check how many replicas are currently reachable/alive (including local)
	countAlive := func() int {
		n.clusterMu.RLock()
		defer n.clusterMu.RUnlock()
		alive := 0
		for _, rurl := range replicas {
			if rurl == n.baseURL {
				alive++
				continue
			}
			if info, ok := n.nodeInfos[rurl]; ok && info.Alive {
				alive++
			}
		}
		return alive
	}

	// If there are fewer alive replicas than the write quorum, wait up to writeWait for them to appear
	if countAlive() < *W {
		timeout := time.After(*writeWait)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
	waitLoop:
		for {
			select {
			case <-ticker.C:
				if countAlive() >= *W {
					break waitLoop
				}
			case <-timeout:
				break waitLoop
			}
		}
		if countAlive() < *W {
			http.Error(w, fmt.Sprintf("insufficient live replicas=%d required=%d", countAlive(), *W), http.StatusServiceUnavailable)
			return
		}
	}

	acks := 0
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, rurl := range replicas {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			if target == n.baseURL {
				n.localPut(item)
				mu.Lock()
				acks++
				mu.Unlock()
				return
			}
			err := n.sendPut(target, item)
			if err == nil {
				mu.Lock()
				acks++
				mu.Unlock()
			} else {
				log.Printf("[%s] put to %s failed: %v", n.baseURL, target, err)
			}
		}(rurl)
	}
	// wait with timeout for W acks
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	timeout := time.After(3 * time.Second)
	select {
	case <-done:
	case <-timeout:
	}
	if acks >= *W {
		w.WriteHeader(200)
		w.Write([]byte(fmt.Sprintf("ok acks=%d", acks)))
	} else {
		http.Error(w, fmt.Sprintf("insufficient acks=%d required=%d", acks, *W), 500)
	}
}

func (n *Node) handleGet(w http.ResponseWriter, r *http.Request) {
	k := r.URL.Query().Get("key")
	if k == "" {
		http.Error(w, "missing key", 400)
		return
	}
	// check local cache
	if v, ok := n.cache.Get(k); ok {
		w.WriteHeader(200)
		w.Write(v)
		return
	}
	replicas := n.findReplicas(k)
	type resp struct {
		item KVItem
		err  error
		url  string
	}
	ch := make(chan resp, len(replicas))
	var wg sync.WaitGroup
	// query upto len(replicas) but stop when R good responses are collected (but we still allow pending to complete for read-repair)
	for _, rurl := range replicas {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			if target == n.baseURL {
				if it, ok := n.localGet(k); ok {
					ch <- resp{item: it, err: nil, url: target}
				} else {
					ch <- resp{err: fmt.Errorf("notfound"), url: target}
				}
				return
			}
			it, err := n.sendGet(target, k)
			if err != nil {
				ch <- resp{err: err, url: target}
			} else {
				ch <- resp{item: it, err: nil, url: target}
			}
		}(rurl)
	}
	// collect
	collected := []resp{}
	timeout := time.After(2 * time.Second)
needLoop:
	for {
		select {
		case rresp := <-ch:
			collected = append(collected, rresp)
			// count successful
			success := 0
			for _, cr := range collected {
				if cr.err == nil {
					success++
				}
			}
			if success >= *R {
				break needLoop
			}
			if len(collected) == len(replicas) {
				break needLoop
			}
		case <-timeout:
			break needLoop
		}
	}
	wg.Wait()
	// pick latest timestamp among successes
	var best KVItem
	got := false
	for _, c := range collected {
		if c.err != nil {
			continue
		}
		if !got || c.item.Timestamp > best.Timestamp {
			best = c.item
			got = true
		}
	}
	if !got {
		http.Error(w, "not found", 404)
		return
	}
	// cache locally
	n.cache.Put(k, best.Value)
	// read-repair: push best to slower/stale replicas asynchronously
	for _, c := range collected {
		if c.err != nil {
			continue
		}
		if c.item.Timestamp < best.Timestamp {
			go func(target string, item KVItem) {
				_ = n.sendPut(target, item)
			}(c.url, best)
		}
	}
	w.WriteHeader(200)
	w.Write(best.Value)
}

// internal replication endpoint
func (n *Node) handleInternalReplicate(w http.ResponseWriter, r *http.Request) {
	var item KVItem
	if err := gob.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, "badgob:"+err.Error(), 400)
		return
	}
	n.localPut(item)
	w.WriteHeader(200)
	w.Write([]byte("replicated"))
}

func (n *Node) handleInternalGet(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if it, ok := n.localGet(key); ok {
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(it); err != nil {
			http.Error(w, "enc error", 500)
			return
		}
		w.WriteHeader(200)
		w.Write(buf.Bytes())
		return
	}
	http.Error(w, "notfound", 404)
}
