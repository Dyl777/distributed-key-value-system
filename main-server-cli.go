package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {

	// CLI-specific flag: allow overriding which node the CLI talks to
	target := flag.String("target", "", "target node URL for CLI requests (overrides -nodes)")
	// parse flags (flag variables are declared in other files)
	flag.Parse()

	// --- if there are remaining args, treat as CLI command ---
	if len(flag.Args()) > 0 {
		// CLI mode
		cmd := flag.Args()[0]
		key := flag.String("key", "", "Key for operation")
		value := flag.String("value", "", "Value for key (set only)")
		flag.CommandLine.Parse(flag.Args()[1:]) // parse flags for this command

		if *key == "" && cmd != "nodes" && cmd != "keys" {
			fmt.Println("Missing --key argument")
			os.Exit(1)
		}

		// Determine target node for CLI requests.
		// Priority: --target flag -> first responsive node from -nodes -> first node from -nodes -> localhost:8000
		var targetNode string
		if *target != "" {
			targetNode = *target
		} else {
			nodes := []string{}
			if *initialNodesCSV != "" {
				nodes = strings.Split(*initialNodesCSV, ",")
			}
			// probe nodes for a responsive /internal/ping
			client := &http.Client{Timeout: 1 * time.Second}
			found := ""
			for _, nurl := range nodes {
				resp, err := client.Get(fmt.Sprintf("%s/internal/ping", nurl))
				if err == nil && resp != nil && resp.StatusCode == 200 {
					resp.Body.Close()
					found = nurl
					break
				}
			}
			if found != "" {
				targetNode = found
			} else if len(nodes) > 0 {
				targetNode = nodes[0]
			} else {
				targetNode = "http://localhost:8000"
			}
		}
		if cmd == "set" || cmd == "put" {
			if *value == "" {
				fmt.Println("Missing --value argument")
				os.Exit(1)
			}
			req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/kv?key=%s", targetNode, *key), strings.NewReader(*value))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Println("Error:", err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			fmt.Println("status:", resp.StatusCode, "body:", string(body))

		} else if cmd == "get" {
			resp, err := http.Get(fmt.Sprintf("%s/kv?key=%s", targetNode, *key))
			if err != nil {
				fmt.Println("Error:", err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			fmt.Println("status:", resp.StatusCode, "body:", string(body))

		} else if cmd == "delete" {
			req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/kv?key=%s", targetNode, *key), nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Println("Error:", err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			fmt.Println("status:", resp.StatusCode, "body:", string(body))

		} else {
			fmt.Println("Unknown command:", cmd)
		}

		return // CLI finished
	}

	// --- NODE MODE: start server ---
	if *port == 0 || *initialNodesCSV == "" {
		fmt.Println("Missing required flags: -port and -nodes for node mode")
		os.Exit(1)
	}

	me := fmt.Sprintf("http://localhost:%d", *port)
	node := NewNode(me)
	initial := strings.Split(*initialNodesCSV, ",")
	found := false
	for _, s := range initial {
		if s == me {
			found = true
			break
		}
	}
	if !found {
		initial = append(initial, me)
	}
	node.setCluster(initial)
	node.startServer()

	// join seed node if not first
	seed := initial[0]
	if seed != me {
		joinURL := fmt.Sprintf("%s/join", seed)
		resp, err := node.httpClient.Post(joinURL, "text/plain", strings.NewReader(me))
		if err != nil {
			log.Printf("join to %s failed: %v", seed, err)
		} else {
			var nodes []string
			if err := json.NewDecoder(resp.Body).Decode(&nodes); err == nil {
				node.setCluster(nodes)
			}
			resp.Body.Close()
		}
	}

	// periodic rebalance/pull
	go func() {
		t := time.NewTicker(5 * time.Second)
		for range t.C {
			node.rebalancePull()
		}
	}()

	select {} // keep node running

}
