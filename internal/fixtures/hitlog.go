package fixtures

import (
	"sort"
	"sync"
)

const (
	// maxHitKeys bounds the number of distinct keys a HitLog retains so a
	// hostile or buggy client cannot grow the map without bound. Any new key
	// appearing after the cap is folded into overflowKey.
	maxHitKeys = 1024
	// overflowKey receives hits for keys beyond maxHitKeys.
	overflowKey = "__overflow__"
	// otherMethod folds any method outside the standard set, so the per-key
	// method breakdown stays bounded no matter what a client sends.
	otherMethod = "OTHER"
)

// HitLog is a concurrency-safe counter of fixture hits.
//
// Keys are request paths with the leading slash removed ("form",
// "form/submit", "app/click", "ws/echo"); the index page uses "/". HTTP
// methods are tracked as a second, also bounded, dimension so tests can tell
// "GET form" from "POST form/submit" when that distinction matters.
//
// Only counters live here: no per-hit payloads, log lines or timestamps, so a
// long soak against one route cannot grow memory per key. The zero value is
// ready to use.
type HitLog struct {
	mu      sync.Mutex
	hits    map[string]int
	methods map[string]map[string]int
}

// newHitLog returns an empty HitLog.
func newHitLog() *HitLog { return &HitLog{} }

// Add records one hit for key.
func (h *HitLog) Add(key string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.bump(key, "")
	h.mu.Unlock()
}

// AddMethod records one hit for key and remembers the HTTP method used.
func (h *HitLog) AddMethod(method, key string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.bump(key, method)
	h.mu.Unlock()
}

// bump must be called with h.mu held.
func (h *HitLog) bump(key, method string) {
	if key == "" {
		key = "/"
	}
	if h.hits == nil {
		h.hits = make(map[string]int)
	}
	if _, seen := h.hits[key]; !seen && len(h.hits) >= maxHitKeys {
		key = overflowKey
	}
	h.hits[key]++
	if method == "" {
		return
	}
	if h.methods == nil {
		h.methods = make(map[string]map[string]int)
	}
	m := h.methods[key]
	if m == nil {
		m = make(map[string]int, 4)
		h.methods[key] = m
	}
	m[method]++
}

// knownMethod reports whether method is one of the standard HTTP methods, so
// the method breakdown cannot be inflated with arbitrary strings.
func knownMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "TRACE", "CONNECT":
		return true
	}
	return false
}

// Count returns how many hits key has. Unknown keys return 0.
func (h *HitLog) Count(key string) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits[key]
}

// MethodCount returns how many of key's hits used the given HTTP method.
func (h *HitLog) MethodCount(method, key string) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if m := h.methods[key]; m != nil {
		return m[method]
	}
	return 0
}

// Total returns the sum of every counter.
func (h *HitLog) Total() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, v := range h.hits {
		n += v
	}
	return n
}

// Reset zeroes every counter and drops the method breakdown.
func (h *HitLog) Reset() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.hits = nil
	h.methods = nil
	h.mu.Unlock()
}

// Keys returns the observed keys in sorted order.
func (h *HitLog) Keys() []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	keys := make([]string, 0, len(h.hits))
	for k := range h.hits {
		keys = append(keys, k)
	}
	h.mu.Unlock()
	sort.Strings(keys)
	return keys
}

// Snapshot returns a copy of the counters.
func (h *HitLog) Snapshot() map[string]int {
	if h == nil {
		return map[string]int{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int, len(h.hits))
	for k, v := range h.hits {
		out[k] = v
	}
	return out
}
