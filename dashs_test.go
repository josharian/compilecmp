package main

import (
	"crypto/sha256"
	"strings"
	"testing"
)

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

func TestExtractNameAndSize(t *testing.T) {
	tests := []struct {
		line string
		name string
		size int
	}{
		{
			line: `reflect.TypeAssert[go.shape.interface { MarshalText() ([]uint8, error) }] STEXT dupok size=423 args=0x18 locals=0x28 funcid=0x0 align=0x0`,
			name: `reflect.TypeAssert[go.shape.interface { MarshalText() ([]uint8, error) }]`,
			size: 423,
		},
		{
			line: `reflect.TypeAssert[go.shape.struct { Name encoding/xml.Name; Value string }] STEXT dupok size=394 args=0x18 locals=0x28 funcid=0x0 align=0x0`,
			name: `reflect.TypeAssert[go.shape.struct { Name encoding/xml.Name; Value string }]`,
			size: 394,
		},
		{
			line: `runtime.main STEXT size=1168 args=0x0 locals=0x80 funcid=0x0 align=0x0`,
			name: `runtime.main`,
			size: 1168,
		},
	}

	for _, test := range tests {
		name, size := extractNameAndSize(test.line)
		if name != test.name || size != test.size {
			t.Errorf("extractNameAndSize(%q) = %q, %d, want %q, %d", test.line, name, size, test.name, test.size)
		}
	}
}

func TestPkgScannerPreservesShapeNames(t *testing.T) {
	s := &pkgScanner{
		Funcs: make(map[string]stextFunc),
		Hash:  sha256.New(),
	}
	lines := []string{
		`reflect.TypeAssert[go.shape.struct { X int }] STEXT dupok size=394 args=0x18 locals=0x28 funcid=0x0 align=0x0`,
		"\t0x0000 00000 (type.go:1) RET",
		`reflect.TypeAssert[go.shape.struct { Y string }] STEXT dupok size=423 args=0x18 locals=0x28 funcid=0x0 align=0x0`,
		"\t0x0000 00000 (type.go:1) RET",
	}
	for _, line := range lines {
		s.ProcessLine([]byte(line))
	}
	s.flush()

	for _, name := range []string{
		`reflect.TypeAssert[go.shape.struct { X int }]`,
		`reflect.TypeAssert[go.shape.struct { Y string }]`,
	} {
		if _, ok := s.Funcs[name]; !ok {
			t.Errorf("missing function %q; got %s", name, strings.Join(sortedKeys(s.Funcs), ", "))
		}
	}
}
