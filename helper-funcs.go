package main

import (
	"hash/crc32"
	"sort"
)


func hashKey(s string) uint32 {
	return crc32.ChecksumIEEE([]byte(s))
}

func sortAndUnique(arr []string) []string {
	set := map[string]bool{}
	for _, a := range arr {
		set[a] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
