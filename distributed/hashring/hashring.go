// Package hashring implements consistent hashing with virtual nodes —
// the shard-assignment scheme behind every distributed cache and
// partitioned store (Dynamo-style systems, memcached clients, Kafka's
// partitioner cousin).
//
// The problem it solves: hashing key % N means adding one node
// remaps *every* key (N → N+1 changes almost all remainders), which
// is a full data migration per rebalance. A consistent hash ring
// remaps only the keys whose successor token changed — and because
// only the joining node adds tokens, those keys are exactly the ones
// the new node now owns.
//
// Design decisions made explicit here:
//
//   - Virtual nodes (tokens) per physical node smooth out the
//     randomness of hashing: more tokens ≈ fairer shares and smaller
//     migration bands, at the cost of a bigger ring.
//   - The ring is a sorted token slice + binary search: lookup is
//     O(log (nodes × vnodes)), rebalance cost lands only on the
//     joining/leaving node.
//   - The hash is FNV-1a: fast and good enough for shard selection.
//     (Production systems may swap in xxhash/murmur; the ring logic
//     does not change.)
package hashring

import (
	"hash/fnv"
	"sort"
	"strconv"
	"sync"
)

type token struct {
	hash uint32
	node string
}

// Ring is a consistent hash ring. It is safe for concurrent use.
type Ring struct {
	mu     sync.RWMutex
	vnodes int
	tokens []token // sorted by hash, unique per (node, vnode)
	nodes  map[string]struct{}
}

// New returns an empty ring with the given number of virtual nodes
// per physical node (a common default is 100–150).
func New(vnodes int) *Ring {
	if vnodes < 1 {
		panic("hashring: vnodes must be >= 1")
	}
	return &Ring{vnodes: vnodes, nodes: make(map[string]struct{})}
}

// Add inserts a node with its virtual nodes. Re-adding a node is a
// no-op.
func (r *Ring) Add(node string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.nodes[node]; ok {
		return
	}
	r.nodes[node] = struct{}{}
	for i := 0; i < r.vnodes; i++ {
		t := token{hash: hash(node + "#" + strconv.Itoa(i)), node: node}
		idx := sort.Search(len(r.tokens), func(j int) bool { return r.tokens[j].hash >= t.hash })
		// Collisions across different nodes are possible but rare with
		// 32-bit hashes and thousands of tokens; on the (astronomically
		// unlikely) exact collision, keep the existing token so the ring
		// never has two tokens at one position for one insertion pass.
		if idx < len(r.tokens) && r.tokens[idx].hash == t.hash {
			continue
		}
		r.tokens = append(r.tokens, token{})
		copy(r.tokens[idx+1:], r.tokens[idx:])
		r.tokens[idx] = t
	}
}

// Remove deletes a node and all its tokens. Removing an absent node
// is a no-op.
func (r *Ring) Remove(node string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.nodes[node]; !ok {
		return
	}
	delete(r.nodes, node)
	kept := r.tokens[:0]
	for _, t := range r.tokens {
		if t.node != node {
			kept = append(kept, t)
		}
	}
	r.tokens = kept
}

// GetNode returns the node that owns key: the first token at or after
// the key's hash position, wrapping around at the end of the ring.
func (r *Ring) GetNode(key string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.tokens) == 0 {
		return ""
	}
	h := hash(key)
	idx := sort.Search(len(r.tokens), func(j int) bool { return r.tokens[j].hash >= h })
	if idx == len(r.tokens) {
		idx = 0
	}
	return r.tokens[idx].node
}

// Nodes returns the physical nodes currently in the ring, sorted.
func (r *Ring) Nodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.nodes))
	for n := range r.nodes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Size reports the number of tokens on the ring (nodes × vnodes).
func (r *Ring) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tokens)
}

func hash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}
