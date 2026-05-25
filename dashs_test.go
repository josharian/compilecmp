package main

import "testing"

func TestStripClosureHash(t *testing.T) {
	tests := map[string]string{
		"pkg.fn#abcDEF012/+=#":            "pkg.fn",
		"pkg.fn#abcDEF012/+=#.1":          "pkg.fn.1",
		"pkg.fn#abcDEF012/+=#.deferwrap1": "pkg.fn.deferwrap1",
		"pkg.fn#abcDEF012/+=#.gowrap1":    "pkg.fn.gowrap1",
		"pkg.fn#abcDEF012/+=#suffix":      "pkg.fn#abcDEF012/+=#suffix",
		"pkg.fn.without.hash":             "pkg.fn.without.hash",
	}
	for in, want := range tests {
		if got := stripClosureHash(in); got != want {
			t.Errorf("stripClosureHash(%q) = %q, want %q", in, got, want)
		}
	}
}
