package kernel

import (
	"strconv"
	"strings"
)

// CompareVersions orders two dotted kernel version strings (e.g. "1.21", "1.9",
// "0.16.1") by segment: each dot-separated segment is compared numerically when
// both sides are numeric (so "1.21" > "1.9"), else lexically. A missing trailing
// segment counts as 0 ("1.2" < "1.2.1"). Returns -1, 0, or +1.
func CompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := max(len(as), len(bs))
	for i := 0; i < n; i++ {
		var x, y string
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if c := compareSegment(x, y); c != 0 {
			return c
		}
	}
	return 0
}

// compareSegment compares one version segment: numerically when both parse as
// integers, otherwise lexically. An empty segment is treated as numeric 0.
func compareSegment(x, y string) int {
	xn, xok := segNum(x)
	yn, yok := segNum(y)
	if xok && yok {
		switch {
		case xn < yn:
			return -1
		case xn > yn:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(x, y)
}

// segNum parses a version segment as a non-negative integer, treating an empty
// segment as 0. ok is false for a non-numeric segment.
func segNum(s string) (int, bool) {
	if s == "" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
