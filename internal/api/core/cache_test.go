package core

import (
	"fmt"
	"testing"
)

func TestMetaCacheBudget(t *testing.T) {
	var c metaCache
	// Ten entries fit, with room for their keys.
	size := metaCacheBudget/10 - 100
	for i := range 10 {
		c.put(fmt.Sprint(i), i, size)
	}
	// Using entry 0 keeps it when the next two push the cache over budget.
	if _, ok := c.get("0"); !ok {
		t.Fatal("entry 0 missing before the budget is reached")
	}
	c.put("10", 10, size)
	c.put("11", 11, size)
	for key, want := range map[string]bool{"0": true, "1": false, "2": false, "3": true, "11": true} {
		if _, ok := c.get(key); ok != want {
			t.Errorf("entry %s cached: %v, want %v", key, ok, want)
		}
	}
	if c.bytes > metaCacheBudget || c.bytes != len(c.entries)*size+entryKeyBytes(&c) {
		t.Errorf("bytes %d for %d entries", c.bytes, len(c.entries))
	}

	c.put("huge", 0, metaCacheBudget/8+1)
	if _, ok := c.get("huge"); ok {
		t.Error("an entry over an eighth of the budget was kept")
	}

	c.put("0", "again", 1)
	if v, _ := c.get("0"); v != "again" || len(c.entries) != c.lru.Len() {
		t.Errorf("replaced entry: %v, %d entries, %d in lru", v, len(c.entries), c.lru.Len())
	}

	c.clear()
	if len(c.entries) != 0 || c.lru.Len() != 0 || c.bytes != 0 {
		t.Errorf("after clear: %d entries, %d in lru, %d bytes", len(c.entries), c.lru.Len(), c.bytes)
	}
}

// entryKeyBytes sums the key lengths of the cached entries.
func entryKeyBytes(c *metaCache) int {
	n := 0
	for k := range c.entries {
		n += len(k)
	}
	return n
}
