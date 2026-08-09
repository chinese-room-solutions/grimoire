package kernel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.21", "1.21", 0},
		{"1.21", "1.9", 1},  // numeric, not lexical: 21 > 9.
		{"1.9", "1.21", -1}, //
		{"1.26", "1.21", 1},
		{"0.16.1", "0.16.0", 1},
		{"0.16.1", "0.16", 1}, // missing trailing segment = 0.
		{"1.2", "1.2.0", 0},   // trailing zero is equal.
		{"2", "1.99", 1},      // major dominates.
		{"1.0", "1", 0},       // "1" == "1.0".
		{"alpha", "beta", -1}, // non-numeric falls back to lexical.
		{"1.0", "1.0", 0},     //
		{"10", "9", 1},        //
		{"3", "3.1", -1},      //
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, CompareVersions(tc.a, tc.b), "CompareVersions(%q, %q)", tc.a, tc.b)
	}
}
