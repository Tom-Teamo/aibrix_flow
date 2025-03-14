package routingalgorithms

import (
	"math"
	"sync"
	"time"
)

// NodeRef is equivalent to Arc<Node> in Rust
type NodeRef = *Node

// Node represents a node in the tree
type Node struct {
	childrenMu sync.RWMutex
	children   map[int]NodeRef // token -> node

	tokensMu sync.RWMutex
	tokens   []int

	tenantLastAccessTimeMu sync.RWMutex
	tenantLastAccessTime   map[string]int64

	parentMu sync.RWMutex
	parent   NodeRef
}

// Tree is a thread-safe multi-tenant radix tree
type Tree struct {
	root               NodeRef
	tenantTokenCountMu sync.RWMutex
	tenantTokenCount   map[string]int
}

// EvictionEntry is used for the eviction priority queue
type EvictionEntry struct {
	timestamp int64
	tenant    string
	node      NodeRef
}

// EvictionHeap implements heap.Interface
type EvictionHeap []EvictionEntry

func (h EvictionHeap) Len() int           { return len(h) }
func (h EvictionHeap) Less(i, j int) bool { return h[i].timestamp < h[j].timestamp }
func (h EvictionHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *EvictionHeap) Push(x interface{}) {
	*h = append(*h, x.(EvictionEntry))
}

func (h *EvictionHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// Helper functions for string operations

// sharedPrefixCount counts the number of matching characters at the beginning of two strings
func sharedPrefixCount(a []int, b []int) int {
	i := 0

	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

// sliceByChars returns a substring based on character (not byte) positions
func sliceByChars(s []int, start, end int) []int {
	if start >= len(s) {
		return []int{}
	}
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// NewTree creates a new Tree
func NewTree() *Tree {
	root := &Node{
		children:             make(map[int]NodeRef),
		tokens:               make([]int, 0),
		tenantLastAccessTime: make(map[string]int64),
		parent:               nil,
	}

	return &Tree{
		root:             root,
		tenantTokenCount: make(map[string]int),
	}
}

// Insert adds tokens to the tree for a specific tenant
func (t *Tree) Insert(tokens []int, tenant string) {
	curr := t.root
	currIdx := 0

	// Get current timestamp in milliseconds
	timestampMs := time.Now().UnixNano() / int64(time.Millisecond)

	// Update tenant last access time for root
	curr.tenantLastAccessTimeMu.Lock()
	curr.tenantLastAccessTime[tenant] = timestampMs
	curr.tenantLastAccessTimeMu.Unlock()

	// Ensure tenant exists in character count map
	t.tenantTokenCountMu.Lock()
	if _, exists := t.tenantTokenCount[tenant]; !exists {
		t.tenantTokenCount[tenant] = 0
	}
	t.tenantTokenCountMu.Unlock()

	prev := t.root
	tokensCount := len(tokens)

	for currIdx < tokensCount {
		firstToken := tokens[currIdx]
		curr = prev

		// Check if child exists for the first rune
		curr.childrenMu.RLock()
		matchedNode, exists := curr.children[firstToken]
		curr.childrenMu.RUnlock()

		if !exists {
			// No match, create a new node
			currtokens := sliceByChars(tokens, currIdx, tokensCount)
			currtokensCount := len(currtokens)

			newNode := &Node{
				children:             make(map[int]NodeRef),
				tokens:               currtokens,
				tenantLastAccessTime: make(map[string]int64),
				parent:               curr,
			}

			// Update tenant character count
			t.tenantTokenCountMu.Lock()
			t.tenantTokenCount[tenant] += currtokensCount
			t.tenantTokenCountMu.Unlock()

			// Update tenant last access time for the new node
			newNode.tenantLastAccessTimeMu.Lock()
			newNode.tenantLastAccessTime[tenant] = timestampMs
			newNode.tenantLastAccessTimeMu.Unlock()

			// Add new node to children
			curr.childrenMu.Lock()
			curr.children[firstToken] = newNode
			curr.childrenMu.Unlock()

			prev = newNode
			currIdx = tokensCount
		} else {
			// Match found, get node tokens
			matchedNode.tokensMu.RLock()
			matchedNodetokens := matchedNode.tokens
			matchedNode.tokensMu.RUnlock()

			matchedNodetokensCount := len(matchedNodetokens)

			currtokens := sliceByChars(tokens, currIdx, tokensCount)
			sharedCount := sharedPrefixCount(matchedNodetokens, currtokens)

			if sharedCount < matchedNodetokensCount {
				// Split the matched node
				matchedtokens := sliceByChars(matchedNodetokens, 0, sharedCount)
				contractedtokens := sliceByChars(matchedNodetokens, sharedCount, matchedNodetokensCount)
				matchedtokensCount := len(matchedtokens)

				// Create a new intermediate node
				newNode := &Node{
					children:             make(map[int]NodeRef),
					tokens:               matchedtokens,
					tenantLastAccessTime: make(map[string]int64),
					parent:               curr,
				}

				// Copy tenant access times from matched node
				matchedNode.tenantLastAccessTimeMu.RLock()
				for t, ts := range matchedNode.tenantLastAccessTime {
					newNode.tenantLastAccessTime[t] = ts
				}
				matchedNode.tenantLastAccessTimeMu.RUnlock()

				// Add the contracted node as child of the new node
				contractedfirstToken := contractedtokens[0]
				newNode.childrenMu.Lock()
				newNode.children[contractedfirstToken] = matchedNode
				newNode.childrenMu.Unlock()

				// Update parent's children
				curr.childrenMu.Lock()
				curr.children[firstToken] = newNode
				curr.childrenMu.Unlock()

				// Update matched node
				matchedNode.tokensMu.Lock()
				matchedNode.tokens = contractedtokens
				matchedNode.tokensMu.Unlock()

				matchedNode.parentMu.Lock()
				matchedNode.parent = newNode
				matchedNode.parentMu.Unlock()

				prev = newNode

				// Update tenant token count if this is a new tenant for this node
				newNode.tenantLastAccessTimeMu.RLock()
				_, hasTenant := newNode.tenantLastAccessTime[tenant]
				newNode.tenantLastAccessTimeMu.RUnlock()

				if !hasTenant {
					t.tenantTokenCountMu.Lock()
					t.tenantTokenCount[tenant] += matchedtokensCount
					t.tenantTokenCountMu.Unlock()
				}

				// Update tenant last access time
				newNode.tenantLastAccessTimeMu.Lock()
				newNode.tenantLastAccessTime[tenant] = timestampMs
				newNode.tenantLastAccessTimeMu.Unlock()

				currIdx += sharedCount
			} else {
				// Move to next node
				prev = matchedNode

				// Update tenant character count if this is a new tenant for this node
				matchedNode.tenantLastAccessTimeMu.RLock()
				_, hasTenant := matchedNode.tenantLastAccessTime[tenant]
				matchedNode.tenantLastAccessTimeMu.RUnlock()

				if !hasTenant {
					t.tenantTokenCountMu.Lock()
					t.tenantTokenCount[tenant] += matchedNodetokensCount
					t.tenantTokenCountMu.Unlock()
				}

				// Update tenant last access time
				matchedNode.tenantLastAccessTimeMu.Lock()
				matchedNode.tenantLastAccessTime[tenant] = timestampMs
				matchedNode.tenantLastAccessTimeMu.Unlock()

				currIdx += sharedCount
			}
		}
	}
}

// PrefixMatch finds the longest matching prefix and associated tenant
func (t *Tree) PrefixMatch(tokens []int) ([]int, string) {
	curr := t.root
	currIdx := 0
	prev := t.root
	tokensCount := len(tokens)

	for currIdx < tokensCount {
		firstToken := tokens[currIdx]
		currtokens := sliceByChars(tokens, currIdx, tokensCount)

		curr = prev

		// Check if there is a child for this rune
		curr.childrenMu.RLock()
		matchedNode, exists := curr.children[firstToken]
		curr.childrenMu.RUnlock()

		if exists {
			// Get node tokens
			matchedNode.tokensMu.RLock()
			matchedNodetokens := matchedNode.tokens
			matchedNode.tokensMu.RUnlock()

			sharedCount := sharedPrefixCount(matchedNodetokens, currtokens)
			matchedNodetokensCount := len(matchedNodetokens)

			if sharedCount == matchedNodetokensCount {
				// Full match with current node's tokens, continue to next node
				currIdx += sharedCount
				prev = matchedNode
			} else {
				// Partial match, stop here
				currIdx += sharedCount
				prev = matchedNode
				break
			}
		} else {
			// No match found, stop here
			break
		}
	}

	curr = prev

	// Select the first tenant (key in the map)
	tenant := "empty"
	curr.tenantLastAccessTimeMu.RLock()
	for t := range curr.tenantLastAccessTime {
		tenant = t
		break
	}
	curr.tenantLastAccessTimeMu.RUnlock()

	// Update timestamps
	timestampMs := time.Now().UnixNano() / int64(time.Millisecond)

	if tenant != "empty" {
		current := curr
		for current != nil {
			current.tenantLastAccessTimeMu.Lock()
			current.tenantLastAccessTime[tenant] = timestampMs
			current.tenantLastAccessTimeMu.Unlock()

			current.parentMu.RLock()
			current = current.parent
			current.parentMu.RUnlock()
		}
	}

	rettokens := sliceByChars(tokens, 0, currIdx)
	return rettokens, tenant
}

// GetSmallestTenant returns the tenant with the smallest character count
func (t *Tree) GetSmallestTenant() string {
	t.tenantTokenCountMu.RLock()
	defer t.tenantTokenCountMu.RUnlock()

	if len(t.tenantTokenCount) == 0 {
		return "empty"
	}

	minTenant := ""
	minCount := math.MaxInt

	for tenant, count := range t.tenantTokenCount {
		if count < minCount {
			minCount = count
			minTenant = tenant
		}
	}

	return minTenant
}

// Additional methods and tests would be implemented similarly
