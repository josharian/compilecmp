package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSelectPlatformsArch(t *testing.T) {
	all := []string{
		"darwin/amd64",
		"js/wasm",
		"linux/amd64",
		"linux/arm64",
		"wasip1/wasm",
	}
	got := selectPlatforms("arch", all)
	want := []string{"js/wasm", "linux/amd64", "linux/arm64"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("selectPlatforms(\"arch\") = %v, want %v", got, want)
	}
}

func TestFilesizesAddedTotal(t *testing.T) {
	var out bytes.Buffer
	sizes := newFilesizes(&out)
	sizes.add("pkg.s", 0, 10)
	sizes.flush("text size")
	if got := out.String(); strings.Contains(got, "Inf") || !strings.Contains(got, "total") {
		t.Fatalf("unexpected added-only total:\n%s", got)
	}
}
