package routingalgorithms

import (
	"context"
	"fmt"
	"sync"
	"time"

	// 导入prefixcacheindexer文件夹下的appro_tree.go

	"github.com/vllm-project/aibrix/pkg/utils"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

var (
	RouterSGLang Algorithms = "sglang"
)

func init() {
	Register(RouterSGLang, func() (Router, error) { return NewSGLangRouter([]string{}) })
}

const (
	CacheThreshold      = 0.5
	BalanceAbsThreshold = 1
	BalanceRelThreshold = 0.1
	TimeoutSecs         = 60
	IntervalSecs        = 10
)

type SGLangTreeCache struct {
	mu      sync.RWMutex
	tree    *Tree
	numPods int
	// allocatedSize []int // not being used. if it is not going to be used, it will be removed permanently.
	allNodes   map[int]*Node
	nextNodeID int
	startTime  time.Time
}

func NewSGLangTreeCache(numPods int) *SGLangTreeCache {
	cache := &SGLangTreeCache{
		numPods: numPods,
		// allocatedSize: make([]int, numPods), // not being used. if it is not going to be used, it will be removed permanently.
		allNodes:   make(map[int]*Node),
		nextNodeID: 0,
		startTime:  time.Now(),
	}
	cache.reset()
	return cache
}

func (c *SGLangTreeCache) reset() {
	root := NewTree()
	c.tree = root
	c.numPods = 0
	c.allNodes = make(map[int]*Node)
	c.nextNodeID = 0
	c.startTime = time.Now()
}

type SGLangRouter struct {
	mu                  sync.RWMutex
	pods_name           []string
	cache               *SGLangTreeCache
	RunningQueue        map[string]int
	ProcessedQueue      map[string]int
	CacheThreshold      float32
	BalanceAbsThreshold int
	BalanceRelThreshold float32
	TimeoutSecs         uint64
	IntervalSecs        uint64
	EvictionThread      *sync.WaitGroup
}

type SGLangRouterConfig struct {
	CacheThreshold       float32
	BalanceAbsThreshold  int
	BalanceRelThreshold  float32
	EvictionIntervalSecs uint64
	MaxTreeSize          int
	TimeoutSecs          uint64
	IntervalSecs         uint64
}

func (p *SGLangRouter) evictionLoop() {
	// TODO: add a eviction based on SGLang Strategy
}

func NewSGLangRouter(pods_name []string) (Router, error) {
	numPods := len(pods_name)
	// Initialize running queue
	runningQueue := make(map[string]int)

	// Initialize processed queue
	processedQueue := make(map[string]int)

	router := &SGLangRouter{
		pods_name:           pods_name,
		cache:               NewSGLangTreeCache(numPods),
		RunningQueue:        runningQueue,
		ProcessedQueue:      processedQueue,
		CacheThreshold:      CacheThreshold,
		BalanceAbsThreshold: BalanceAbsThreshold,
		BalanceRelThreshold: BalanceRelThreshold,
		TimeoutSecs:         TimeoutSecs,
		IntervalSecs:        IntervalSecs,
		EvictionThread:      &sync.WaitGroup{},
	}

	// Start eviction ticker
	go router.evictionLoop()

	return router, nil
}

func (c *SGLangTreeCache) GetAllNodes() map[int]*Node {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.allNodes
}

func (n *Node) RemovePodsNotInSet(currentPodSet map[string]bool) bool {
	//TODO
	return false
}

func (n *Node) ResetEvictedPods() {
	// TODO
	// n.evictedPods = make(map[int]bool)
}

func (n *Node) ResetCachedPods() {
	// TODO
	// n.cachedPods = make(map[int]bool)
}

func (n *Node) ResetRefCounter(numPods int) {
	// TODO
	// n.refCounter = make([]int, numPods)
}

func (p *SGLangRouter) updatePodSet(readyPods []*v1.Pod) {
	currentPodSet := make(map[string]bool)
	for _, pod := range readyPods {
		currentPodSet[pod.Name] = true
	}
	allNodes := p.cache.GetAllNodes()
	podsChanged := false
	// Update cache structures
	for _, node := range allNodes {
		// 1. Update ModelToPods
		if node.RemovePodsNotInSet(currentPodSet) {
			podsChanged = true
		}
		// 2. Update node's pod-specific data structures
		node.ResetEvictedPods()                  // Reset as pod IDs might change
		node.ResetCachedPods()                   // Reset as pod IDs might change
		node.ResetRefCounter(len(currentPodSet)) // Resize for new pod count
	}

	// Update router and histogram if pods changed
	if podsChanged || len(currentPodSet) != p.cache.numPods {
		// klog.InfoS("Pod set updated", "old_count", p.numPods, "new_count", len(currentPodSet))
		// // Update router structures
		// p.numPods = len(currentPodSet)
		// p.podAllocations = make(map[*prefixcacheindexer.TreeNode]map[int]bool)

		// // Update histogram structures
		// h := p.histogram
		// h.mu.Lock()
		// defer h.mu.Unlock()

		// h.numPods = len(currentPodSet)

		// // Clean up pod-specific maps
		// for podName := range h.currentDecodeLengthsPerPod {
		// 	if !currentPodSet[podName] {
		// 		delete(h.currentDecodeLengthsPerPod, podName)
		// 		delete(h.avgTimePerTokenPerPod, podName)
		// 	}
		// }

		// // Reset pod allocation maps
		// h.podAllocations = make(map[*prefixcacheindexer.TreeNode]map[int]bool)

	}
}

func (sgl *SGLangRouter) Route(ctx context.Context, pods map[string]*v1.Pod, model, message string) (string, error) {
	readyPods := utils.FilterReadyPods(pods)

	if len(readyPods) == 0 {
		return "", fmt.Errorf("no pods to forward request")
	}

	if len(readyPods) == 1 {
		for _, pod := range readyPods {
			return getPodAddress(pod.Status.PodIP)
		}
	}

	sgl.mu.Lock()
	defer sgl.mu.Unlock()

	// First, update pod set
	klog.Infof("num pods in data structure: %d", sgl.cache.numPods)
	klog.Infof("current actual ready pods: %d", len(readyPods))

	sgl.updatePodSet(readyPods)
	klog.Infof("num pods in data structure after updatePodSet: %d", sgl.cache.numPods)

	trimmedMessage := utils.TrimMessage(message)
	klog.Infof("Trimmed message: '%s'", trimmedMessage)
	tokens, err := utils.TokenizeInputText(trimmedMessage)
	if err != nil {
		return "", err
	}
	klog.Info("AddPrefix to the tree: ", tokens)

	// Get current load statistics
	maxLoad := 0
	minLoad := int(^uint(0) >> 1) // Max int value
	for _, load := range sgl.RunningQueue {
		if load > maxLoad {
			maxLoad = load
		}
		if load < minLoad {
			minLoad = load
		}
	}

	// Load is considered imbalanced if:
	// 1. (max - min) > abs_threshold AND
	// 2. max > rel_threshold * min
	isImbalanced := (maxLoad-minLoad) > sgl.BalanceAbsThreshold &&
		float32(maxLoad) > float32(minLoad)*sgl.BalanceRelThreshold

	var selectedPod string
	if isImbalanced {
		// Log load balancing trigger and current queue state
		klog.Infof("Load balancing triggered due to workload imbalance:\nMax load: %d, Min load: %d\nCurrent running queue: %+v", maxLoad, minLoad, sgl.RunningQueue)

		// Use shortest queue routing when load is imbalanced
		minLoad := int(^uint(0) >> 1) // Max int value
		for pod, count := range sgl.RunningQueue {
			if count < minLoad {
				minLoad = count
				selectedPod = pod
			}
		}
	} else {
		// Use cache-aware routing when load is balanced
		matchedTokens, pod := sgl.cache.tree.PrefixMatch(tokens)
		matchedRate := float32(len(matchedTokens)) / float32(len(tokens))

		if matchedRate > sgl.CacheThreshold {
			selectedPod = pod
		} else {
			selectedPod = sgl.cache.tree.GetSmallestTenant()
		}
	}

	// Update queues and tree
	sgl.RunningQueue[selectedPod]++

	sgl.ProcessedQueue[selectedPod]++

	sgl.cache.tree.Insert(tokens, selectedPod)

	return selectedPod, nil
}
