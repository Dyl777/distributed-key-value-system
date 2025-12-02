package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"io"
)

// ---------- network calls for replication ----------
func (n *Node) sendPut(target string, item KVItem) error {
	// send binary gob
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(item); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/internal/replicate", target)
	resp, err := n.httpClient.Post(url, "application/octet-stream", &buf)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("replicate status %d", resp.StatusCode)
	}
	return nil
}

func (n *Node) sendGet(target, key string) (KVItem, error) {
	url := fmt.Sprintf("%s/internal/get?key=%s", target, key)
	resp, err := n.httpClient.Get(url)
	if err != nil {
		return KVItem{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return KVItem{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var item KVItem
	if err := gob.NewDecoder(resp.Body).Decode(&item); err != nil {
		return KVItem{}, err
	}
	return item, nil
}
