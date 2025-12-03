# Distributed Key-Value System

A simple, distributed key-value store implemented in Go I have currently spent 2 weeks working on it. It demonstrates
replication (W/R quorums), membership/heartbeating, a small deterministic replica
selection ring, last-write-wins timestamps, and an in-memory LRU cache for hot reads.

License: GNU GPLv3 (see file headers in source files)

---

## Requirements

- Go 1.17+ (1.20+ recommended)

## Build

From the project root run:

```powershell
gofmt -w .
go build
```

Or to run directly from source (builds the package):

```powershell
go run . --help
```

## Run / Example (PowerShell)

Start two nodes (separate terminals):

Terminal A:

```powershell
go run . -port=8000 -nodes=http://localhost:8000,http://localhost:8001 -replication=2 -W=2 -R=1
```

Terminal B:

```powershell
go run . -port=8001 -nodes=http://localhost:8000,http://localhost:8001 -replication=2 -W=2 -R=1
```

Use the built-in CLI mode or HTTP to interact with the cluster.

CLI examples (from project root):

```powershell
# Put a key via the client CLI
go run . put --key=foo --value=bar

# Get a key via the client CLI
go run . get --key=foo
```

HTTP examples (curl or PowerShell `Invoke-RestMethod`):

```powershell
# Put (HTTP)
Invoke-RestMethod -Method Put -Uri 'http://localhost:8000/kv?key=foo' -Body 'bar'

# Get (HTTP)
Invoke-RestMethod -Method Get -Uri 'http://localhost:8000/kv?key=foo'
```

If a node is down, the client will try to auto-select a responsive target from the
`-nodes` list or you can explicitly set a target with `--target=http://host:port`.

## Important flags

- `-port` : port the node listens on (ex: `8000`).
- `-nodes` : comma-separated initial node URLs (ex: `http://localhost:8000,http://localhost:8001`).
- `-replication` : replication factor (number of replicas for each key).
- `-W` : write quorum (how many replicas must ack a write).
- `-R` : read quorum (how many replicas to consult for reads).
- `-heartbeat` : heartbeat interval (default `2s`) used for liveness checks.
- `-cache-size` : LRU cache size for hot reads.
- `-write-wait` : duration to wait for enough live replicas before failing a write (default `3s`).
- `--target` : client-only; explicitly pick a node to contact.

Flags can be adjusted to experiment with different failure modes and quorum behaviors.

## Routes

This section documents the HTTP endpoints served by a node and their purpose.

- `GET /kv?key=<key>` — client-facing read. Reads consult up to the configured replica
  set and use the read quorum `R`. Responses are raw value bytes (200) or 404 if not found.
- `PUT /kv?key=<key>` — client-facing write. Body is raw bytes to store. Writes attempt to
  replicate to the configured replicas and require `W` acks. If insufficient live replicas
  are available after waiting up to `-write-wait`, the server returns `503 Service Unavailable`.

Internal endpoints (used for node-to-node communication):

- `POST /internal/replicate` — internal replication endpoint. Expects a gob-encoded `KVItem`.
  Used to push updates to replicas.
- `GET /internal/get?key=<key>` — internal read used by peers to fetch a single `KVItem` (gob).
- `GET /internal/ping` — lightweight liveness ping used by the heartbeat loop.
- `GET /internal/keys` — returns locally-held keys (used during rebalancing/pull operations).

Cluster management endpoints:

- `POST /join` — used by a node joining the cluster to announce itself; the receiving node
  will incorporate the joining node into its cluster view and initiate rebalancing as needed.
- `GET /nodes` — returns the current known node list and simple metadata.

## Reconnection & Liveness Strategy

This project uses a heartbeat-driven liveness approach and a few client-side
strategies to reduce false positives and improve availability during transient failures.

- Heartbeat interval and failures: nodes ping peers at `-heartbeat` (default `2s`). The
  implementation requires multiple consecutive missed pings before marking a peer down
  (default threshold: `3` consecutive failures). This reduces flapping caused by brief
  network hiccups.
- `FailCount`: each peer has a small counter of consecutive failed heartbeats. When the
  counter reaches the configured threshold, the node is marked not alive and will be
  excluded from replica selection until it responds to pings again.
- Join behavior: when a node successfully `POST /join` to another node, the receiving
  node resets its stored metadata for that joining node (marks it alive and clears failure
  counters) to avoid stubbornly treating re-joining nodes as dead.
- Write waiting: when a write arrives and fewer than `W` replicas are currently alive,
  the writer waits up to `-write-wait` (default `3s`) for replicas to become reachable.
  If the required `W` replicas do not become available in that window, the write returns
  `503` instead of trying and failing to reach quorum.
- Client target selection & retries: the CLI will attempt to contact a configured `--target`
  if provided. If not provided, it probes the `-nodes` list to find the first responsive
  node and uses that as the request target. In case of transient failures, retries or
  re-running the client command after a short wait is a practical approach.

When a peer is unreachable, nodes will attempt to re-contact it on subsequent heartbeat
intervals. Re-joining nodes should call `/join` to re-announce themselves (the CLI and
startup logic in the node mode do this automatically).

## Behavior & Architecture

- Replica selection: a deterministic ring from a sorted list of nodes is used to pick
  replica targets for a key (simple demo of consistent placement semantics).
- Quorum: reads and writes use configurable `W` and `R` values. The system enforces
  the configured quorum for writes and returns `503 Service Unavailable` if a write
  cannot reach the required `W` replicas after waiting up to `-write-wait`.
- Heartbeating: nodes periodically ping peers; nodes must miss several consecutive
  heartbeats before being marked down to reduce false positives.
- Rebalance: when membership changes, nodes pull keys they should now own from peers.
- Cache: an in-memory LRU cache speeds up hot reads and is thread-safe.

## Notes & Caveats

- This is an educational project, it does not provide strict consistency, security,
  persistence, or production-grade cluster management.
- Tests / integration harnesses are not included by default; run multiple local nodes
  (different ports) to experiment.


## Planned / Future Features

Planned improvements and features that would make this demo more useful in real or
intranet environments:

- Support intranet IPs and non-localhost deployments: advertise and bind to LAN
  addresses (e.g. `192.168.x.x`), allow configuring advertised URLs separate from the
  listen address, and provide guidance for firewall/NAT traversal when nodes are on
  different subnets.
- Node discovery: add optional local network discovery (mDNS/UDP) or a simple
  discovery seed service so nodes can find each other without manually listing
  `-nodes`.
- Secure communication: TLS for node-to-node and client-to-node connections and
  optional authentication for join requests to avoid unauthorized cluster changes.
- Improved NAT traversal and port-forwarding guidance: document recommended router
  settings or add an optional STUN-like mechanism for crossing NATs.

If you'd like, I can add a short section describing how to bind/advertise a different
address than the listen port (example config) and a small PowerShell script to
demonstrate starting nodes on LAN IPs — a sample script is included at
`scripts/start-local.ps1`.

### Example: bind to an intranet IP and advertise a different URL

Notes:

- The server listens on `:%d` (all interfaces) by default using the `-port` flag, so
  it will accept connections on a LAN IP (for example, `192.168.1.100`) as long as
  the host machine has that address assigned and firewall rules permit it.
- To ensure the cluster advertises LAN addresses (instead of `localhost`), you can
  either:
  1. Provide the LAN URLs in the `-nodes` list and manually POST the advertised
     LAN URL to the seed's `/join` endpoint so the seed stores the correct advertised
     address, or
  2. Modify the startup logic to compute an "advertised URL" (e.g. add an
     `-advertise` flag) and use that when sending join requests. The simplest
     non-code approach is (1) which the included script automates.

Example manual sequence (replace `192.168.1.100` with your LAN IP):

```powershell
# Start first node (seed) on port 8000; it will listen on all interfaces
go run . -port=8000 -nodes=http://192.168.1.100:8000,http://192.168.1.100:8001 -replication=2 -W=2 -R=1

# Start second node on same machine or another machine with the same LAN IP
go run . -port=8001 -nodes=http://192.168.1.100:8000,http://192.168.1.100:8001 -replication=2 -W=2 -R=1

# From any machine, inform the seed about the second node using its LAN URL
Invoke-RestMethod -Method Post -Uri 'http://192.168.1.100:8000/join' -Body 'http://192.168.1.100:8001'
```

The short PowerShell helper `scripts/start-local.ps1` bundles this pattern: it
starts multiple `go run` instances (each in its own PowerShell window) using the
LAN IP you provide and then posts the advertised LAN URLs to the seed's `/join` so
the cluster view contains the LAN addresses rather than `localhost`.

### Prioritized Future Work (short notes)

1) Support intranet IPs / advertised addresses (high priority)
   - Why: local network testing and small LAN deployments are common; currently
     the node advertises `localhost` which is not usable across machines.
   - Implementation: add a small `-advertise` flag (or env var) to `main` and use
     that value when constructing the node's base URL and when posting `/join`.
   - Tests: start nodes on different machines in the same LAN and verify `/nodes`
     lists the LAN addresses.

2) Simple discovery (medium priority)
   - Why: manual `-nodes` configuration is tedious and error-prone in LANs.
   - Implementation: optional mDNS or UDP-based gossip discovery that populates
     seed URLs at startup; keep `-nodes` as a manual fallback.

3) TLS + authentication (high priority for non-demo use)
   - Why: prevents eavesdropping and unauthorized cluster changes.
   - Implementation: support TLS certs for server and client; sign join requests
     using a shared token or mTLS in trusted environments.

4) NAT traversal / cloud-friendly operation (lower priority)
   - Why: making the demo work across NATs or across subnets requires NAT/STUN
     guidance or an external rendezvous service.
   - Implementation: document recommended port-forwarding or add a simple STUN-like
     helper service to discover public IP when needed.

If you'd like, I can implement item (1) — add `-advertise` and wire it into the
startup/join logic — and then update the script so it does not need to POST `/join`.

## Locking & Concurrency

Summary of the important locks and why they exist (see `datatypes-utils.go` and `key-vl-dist-impl.go`):

- `mu (sync.RWMutex)` on `Node`: protects the local key/value `store` map. It is used as
  an RW lock to allow many concurrent readers (`localGet`, read handlers) while writers
  (`localPut`) acquire the exclusive lock for read-modify-write sequences. `localPut`
  updates the LRU cache while holding `mu` to keep store+cache state consistent; the
  cache itself has its own internal mutex.
- `clusterMu (sync.RWMutex)` on `Node`: protects cluster metadata (`nodes` slice and
  `nodeInfos` map). Request handlers read these concurrently while background tasks
  (heartbeat, join handling) update them. Using a separate lock reduces contention
  between request handling and membership maintenance.
- LRU cache mutex (`LRUCache.mu`): a simple mutex guards the cache data structures (map
  + linked list). The cache is intentionally independent so cache contention does not
  block store-level locks.

Design principles used in the code base:

- Keep critical sections small. Functions like `getNodes()` return a snapshot copy of the
  membership under `clusterMu.RLock()`, allowing callers to work on a stable view without
  holding the lock for the entire operation.
- Avoid holding exclusive store locks across network I/O. `rebalancePull` and replication
  logic try to perform network calls without holding the exclusive `mu` lock; writes are
  performed via `localPut` which acquires `mu` only when necessary.
- Use per-component locks (store vs cluster vs cache) so unrelated operations do not
  serialize on a single global lock.

## Execution Flow (Activity Diagram) (what happens when you run the common commands)

Step-through of what the program does in memory for the common
commands you run from the README examples.

1) `gofmt -w .` / `go build`
  - `gofmt` reformats source files in place (no runtime effects). `go build` compiles
    the package into an executable. No program logic runs during these steps.

2) `go run . -port=8000 -nodes=...` (start node process)
  - The `main` function parses package-level flags (declared in other files) and enters
    node mode because no CLI command was passed.
  - It constructs the node base URL (e.g. `http://localhost:8000`) and calls
    `NewNode(me)` which allocates `Node` fields: `store`, `nodeInfos`, `cache`, and an
    `httpClient` (safe for concurrent use).
  - `node.setCluster(initial)` initializes `nodes` and `nodeInfos` under `clusterMu`.
  - `node.startServer()` sets up HTTP handlers and starts the HTTP server in a goroutine.
    It also starts the `heartbeatLoop` in another goroutine.
  - If the configured seed node is different (`seed != me`), the node posts to
    `POST /join` on the seed. The seed will add this node to its view and return the
    cluster list; the joining node updates its `nodes` accordingly.
  - A background rebalance goroutine periodically calls `rebalancePull()` to fetch
    keys this node should now hold.
  - The process remains running, serving HTTP and background loops.

3) `go run . put --key=foo --value=bar` (CLI put)
  - The `main` detects CLI mode (an extra command argument). The CLI parses its own
    flags (`--key`, `--value`, `--target`).
  - CLI determines a target node URL (explicit `--target` → probe `-nodes` for a
    responsive `/internal/ping` → fallback to first `-nodes` entry → `http://localhost:8000`).
  - The CLI issues an HTTP `PUT /kv?key=foo` with the provided value to the chosen node.
  - On the receiving node, `handlePut` runs:
    - It builds a `KVItem` with the current timestamp.
    - It calls `findReplicas(key)` (which snapshots `nodes` under `clusterMu.RLock()`),
      to get the replica list for the key.
    - If there are fewer currently-alive replicas than the configured `W`, the handler
      will wait up to `-write-wait` for replicas to appear (probing `nodeInfos` under
      `clusterMu.RLock()`/RLock semantics) and return `503` if quorum cannot be met.
    - It issues parallel replication requests (local writes via `localPut` and
      networked replicates via `/internal/replicate`). Each local write grabs `mu`.
    - The handler waits for acknowledgements and responds to the client depending on
      whether `W` acks were achieved.

4) `go run . get --key=foo` (CLI get)
  - The CLI picks a target node the same way as `put`, then issues `GET /kv?key=foo`.
  - The receiving node's `handleGet`:
    - Checks the local cache first (cache has its own mutex).
    - Calls `findReplicas` to get the list of replicas and concurrently queries them
      using `/internal/get` (or local `localGet`). These network calls are done in
      parallel; the handler collects up to `R` successful responses or times out.
    - It selects the latest timestamped `KVItem` among successful responses and returns
      the value. It also performs asynchronous read-repair by pushing the latest value to
      stale replicas.

Notes on failure handling and retries:

- Heartbeat-based liveness means a node briefly unreachable will not be instantly
  removed; clients and handlers use `-write-wait` and small timeouts to tolerate
  transient failures.
- The CLI probes nodes for `/internal/ping` to choose a responsive target, and you can
  manually override behavior with `--target` if desired.

## CI/CD, Benchmarking & Comparable Projects


- Use GitHub Actions (or similar) to run `gofmt`, `go vet`, `go test`, and `golangci-lint`
  on pull requests. Add a matrix for Go versions to ensure compatibility (e.g. 1.20, 1.21).
- Add integration job(s) that spin up multiple nodes (via Docker Compose or the
  included `scripts/start-local.ps1`) and run a small smoke test to validate join/put/get
  behavior on merges to `main` or nightly.
- Publish build artifacts or a small test image for reproducible benchmarking.

Benchmarking guidance:

- Use `go test -bench . -run ^$` for pure in-process benchmarks (e.g. cache, local store
  operations). For networked benchmarks (throughput/latency across nodes) use external
  tools such as `wrk` or `vegeta` and scripted scenarios:

  - Write-heavy: many concurrent PUTs to the cluster.
  - Read-heavy: many concurrent GETs with cache warmup vs cold reads.
  - Churn: introduce artificial delays or kill/restart nodes to measure availability impact.

- Capture metrics: p50/p95/p99 latency, throughput (ops/sec), and error rate. Use
  `benchstat` (golang.org/x/perf/cmd/benchstat) or a simple CSV-based logger for comparison.
- Automated benchmarking in CI is useful but noisy; consider nightly benchmark jobs
  and keep reproducible artifacts (Docker images or pinned binaries) for fair comparisons.

### Parts I used AI
How to implement an cache (LRUCache)
GNU License Comments