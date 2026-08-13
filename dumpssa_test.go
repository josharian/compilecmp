package main

import "testing"

func TestSSADumpPath(t *testing.T) {
	tests := map[string]string{
		"dumped SSA to ./ssa.html":                             "./ssa.html",
		"dumped SSA for (*decoder).processDHT,1 to ./ssa.html": "./ssa.html",
	}
	for line, want := range tests {
		got, ok := ssaDumpPath(line)
		if !ok || got != want {
			t.Errorf("ssaDumpPath(%q) = %q, %v, want %q, true", line, got, ok, want)
		}
	}
}

func TestSplitPkgFnname(t *testing.T) {
	cases := []struct {
		in, pkg, fn string
	}{
		{"a/b.c", "a/b", "c"},
		{"runtime.main", "runtime", "main"},
		{"net/http.(*conn).serve", "net/http", "(*conn).serve"},
		{"cmd/compile/internal/ssa.(*state).stmt", "cmd/compile/internal/ssa", "(*state).stmt"},
		{"(*scanner).digits:*", "", "(*scanner).digits:*"},
	}

	for _, test := range cases {
		pkg, fn := splitPkgFnname(test.in)
		if pkg != test.pkg || fn != test.fn {
			t.Errorf("splitPkgFnname(%q)=%q, %q, want %q, %q", test.in, pkg, fn, test.pkg, test.fn)
		}
	}
}
