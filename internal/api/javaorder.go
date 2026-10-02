package api

// javaHashMapOrder returns keys in the iteration order of a java.util.HashMap
// they were inserted into (in the given order). Several Java endpoints build
// their result lists by grouping into a HashMap, and the frontend keeps that
// order for equal counts, so matching it keeps ties ordered as in cBioPortal.
func javaHashMapOrder(keys []string) []string {
	capacity := 16
	for float64(len(keys)) > 0.75*float64(capacity) {
		capacity *= 2
	}
	buckets := make([][]string, capacity)
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		i := javaSpread(javaStringHash(k)) & int32(capacity-1)
		buckets[i] = append(buckets[i], k)
	}
	out := make([]string, 0, len(seen))
	for _, b := range buckets {
		out = append(out, b...)
	}
	return out
}

// javaStringHash is java.lang.String.hashCode over UTF-16 code units.
func javaStringHash(s string) int32 {
	var h int32
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			h = 31*h + int32(0xD800+(r>>10))
			h = 31*h + int32(0xDC00+(r&0x3FF))
			continue
		}
		h = 31*h + int32(r)
	}
	return h
}

// javaSpread is HashMap.hash: the high bits folded into the low bits.
func javaSpread(h int32) int32 {
	return h ^ int32(uint32(h)>>16)
}
