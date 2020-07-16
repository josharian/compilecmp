package main

import "testing"

func TestSplitPkgFnname(t *testing.T) {
	cases := []struct {
		in, pkg, fn string
	}{
		{"a/b.c", "a/b", "c"},
		{"(*scanner).digits:*", "", "(*scanner).digits:*"},
	}

	for _, test := range cases {
		pkg, fn := splitPkgFnname(test.in)
		if pkg != test.pkg || fn != test.fn {
			t.Errorf("splitPkgFnname(%q)=%q, %q, want %q, %q", test.in, pkg, fn, test.pkg, test.fn)
		}
	}
}
