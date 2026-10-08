package core

import "github.com/cbioportal/cbioportal-go/internal/javaorder"

// JavaHashMapOrder returns keys in the iteration order of a java.util.HashMap
// they were inserted into (in the given order). Several Java endpoints build
// their result lists by grouping into a HashMap, and the frontend keeps that
// order for equal counts, so matching it keeps ties ordered as in cBioPortal.
func JavaHashMapOrder(keys []string) []string { return javaorder.HashMapOrder(keys) }

// JavaStringHash is java.lang.String.hashCode over UTF-16 code units.
func JavaStringHash(s string) int32 { return javaorder.StringHash(s) }

// JavaSpread is HashMap.hash: the high bits folded into the low bits.
func JavaSpread(h int32) int32 { return javaorder.Spread(h) }
