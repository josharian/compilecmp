package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// closureHashRE matches the content-hash suffix the compiler appends to
// closures inside generic functions, e.g. "func1#5tD4ql0F/hs=" or
// "func1#5tD4ql0F/hs=#". The hash can be followed by compiler-generated
// wrapper suffixes like ".1" or ".deferwrap1", so don't require it to appear
// at the end of the symbol.
var closureHashRE = regexp.MustCompile(`#[A-Za-z0-9+/]{8,}=*#?(\.|$)`)

func stripClosureHash(name string) string {
	return closureHashRE.ReplaceAllString(name, "$1")
}

func compareFunctions(platform string, before, after commit) {
	await, ascan := streamDashS(platform, before)
	bwait, bscan := streamDashS(platform, after)
	compareFuncReaders(ascan, bscan, before.sha, after.sha)
	await()
	bwait()
}

func compareFuncReaders(a, b io.Reader, aHash, bHash string) {
	sizesBuf := new(bytes.Buffer)
	sizes := newFilesizes(sizesBuf)

	aChan := make(chan *pkgScanner)
	go scanDashS(a, []byte(aHash), aChan)
	bChan := make(chan *pkgScanner)
	go scanDashS(b, []byte(bHash), bChan)
	aPkgs := make(map[string]*pkgScanner)
	bPkgs := make(map[string]*pkgScanner)
	done := make(chan struct{}, 2)
	go func() {
		for p := range aChan {
			aPkgs[p.Name] = p
		}
		done <- struct{}{}
	}()
	go func() {
		for p := range bChan {
			bPkgs[p.Name] = p
		}
		done <- struct{}{}
	}()
	<-done
	<-done

	// Process packages in deterministic (sorted) order so that output
	// is stable across runs.
	pkgNames := make([]string, 0, len(aPkgs)+len(bPkgs))
	seen := make(map[string]bool, len(aPkgs)+len(bPkgs))
	for name := range aPkgs {
		if !seen[name] {
			seen[name] = true
			pkgNames = append(pkgNames, name)
		}
	}
	for name := range bPkgs {
		if !seen[name] {
			seen[name] = true
			pkgNames = append(pkgNames, name)
		}
	}
	sort.Strings(pkgNames)

	for _, pkg := range pkgNames {
		aPkg, aOk := aPkgs[pkg]
		bPkg, bOk := bPkgs[pkg]
		if !aOk {
			log.Printf("package %s was added", pkg)
			continue
		}
		if !bOk {
			log.Printf("package %s was deleted", pkg)
			continue
		}
		delete(aPkgs, pkg)
		delete(bPkgs, pkg)

		needsHeader := true
		printHeader := func() {
			if !needsHeader {
				return
			}
			fmt.Printf("\n%s%s%s%s\n", ansiFgYellow, ansiBold, pkg, ansiReset)
			needsHeader = false
		}

		var aTot, bTot int
		// Helper to compare a matched pair and produce the same output
		// the original code did inside the exact-name loop.
		compare := func(aName, bName string, asf, bsf stextFunc) {
			if bytes.Equal(asf.bodyhash, bsf.bodyhash) {
				return
			}
			// TODO: option to show these
			if asf.textsize == bsf.textsize {
				if *flagFn == "all" {
					printHeader()
					fmt.Print(ansiFgBlue)
					fmt.Println(aName, "changed")
					fmt.Print(ansiReset)
				}
				return
			}
			color := ""
			show := true
			if asf.textsize < bsf.textsize {
				if *flagFn == "smaller" || *flagFn == "stats" {
					show = false
				}
				color = ansiFgRed
			} else {
				if *flagFn == "bigger" || *flagFn == "stats" {
					show = false
				}
				color = ansiFgGreen
			}
			if show {
				printHeader()
				fmt.Print(color)
				pct := 100 * (float64(bsf.textsize)/float64(asf.textsize) - 1)
				dispName := cleanFuncName(aName)
				if aName != bName {
					dispName = fmt.Sprintf("%s -> %s", cleanFuncName(aName), cleanFuncName(bName))
				}
				fmt.Printf("%s %d -> %d  (%+0.2f%%)\n", dispName, asf.textsize, bsf.textsize, pct)
				fmt.Print(ansiReset)
			}
		}

		// Pass 1: exact-name match. Walk in sorted order for stable output.
		aLeft := make(map[string]stextFunc, len(aPkg.Funcs))
		for _, name := range sortedKeys(aPkg.Funcs) {
			asf := aPkg.Funcs[name]
			aTot += asf.textsize
			bsf, ok := bPkg.Funcs[name]
			if !ok {
				aLeft[name] = asf
				continue
			}
			delete(bPkg.Funcs, name)
			bTot += bsf.textsize
			compare(name, name, asf, bsf)
		}
		// bPkg.Funcs now holds the b-side leftovers.
		bLeft := bPkg.Funcs

		// Pass 2: match leftovers by stripped name (closure hash removed).
		// Catches closures whose body changed but whose enclosing function
		// did not. If multiple leftovers strip to the same key, pick the
		// lexicographically first one to keep matching deterministic.
		bByStripped := make(map[string][]string, len(bLeft))
		for name := range bLeft {
			s := stripClosureHash(name)
			if s == name {
				continue
			}
			bByStripped[s] = append(bByStripped[s], name)
		}
		for _, names := range bByStripped {
			sort.Strings(names)
		}
		matched2 := make([]string, 0)
		for _, aName := range sortedKeys(aLeft) {
			aStripped := stripClosureHash(aName)
			if aStripped == aName {
				continue
			}
			cands := bByStripped[aStripped]
			if len(cands) == 0 {
				continue
			}
			bName := cands[0]
			bByStripped[aStripped] = cands[1:]
			asf := aLeft[aName]
			bsf := bLeft[bName]
			bTot += bsf.textsize
			delete(bLeft, bName)
			matched2 = append(matched2, aName)
			compare(aName, bName, asf, bsf)
		}
		for _, n := range matched2 {
			delete(aLeft, n)
		}

		// Pass 3: match leftovers by bodyhash. Catches closures whose body
		// is unchanged but whose enclosing function was re-attributed by
		// the linker's closure-dedup pass (different "winner" between
		// builds). Bodyhash equality means the disassembly is identical,
		// so attribution to each other is correct.
		bByHash := make(map[string][]string, len(bLeft))
		for name, sf := range bLeft {
			bByHash[string(sf.bodyhash)] = append(bByHash[string(sf.bodyhash)], name)
		}
		for _, names := range bByHash {
			sort.Strings(names)
		}
		matched3 := make([]string, 0)
		for _, aName := range sortedKeys(aLeft) {
			asf := aLeft[aName]
			cands := bByHash[string(asf.bodyhash)]
			if len(cands) == 0 {
				continue
			}
			bName := cands[0]
			bByHash[string(asf.bodyhash)] = cands[1:]
			bsf := bLeft[bName]
			bTot += bsf.textsize
			delete(bLeft, bName)
			matched3 = append(matched3, aName)
			// Bodies are identical (same hash); compare is a no-op, but
			// nothing to print regardless.
			_ = bsf
		}
		for _, n := range matched3 {
			delete(aLeft, n)
		}

		// Anything still in aLeft is genuinely deleted; bLeft, inserted.
		if *flagFn != "stats" {
			for _, name := range sortedKeys(aLeft) {
				printHeader()
				fmt.Println("deleted", cleanFuncName(name))
			}
			for _, name := range sortedKeys(bLeft) {
				printHeader()
				fmt.Println("inserted", cleanFuncName(name))
			}
		}
		for _, name := range sortedKeys(bLeft) {
			bTot += bLeft[name].textsize
		}
		sizes.add(pkg+".s", int64(aTot), int64(bTot))
		// TODO: option to print these
		// printHeader()
		// if aTot == bTot {
		// 	fmt.Print(ansiFgBlue)
		// } else if aTot < bTot {
		// 	fmt.Print(ansiFgRed)
		// } else {
		// 	fmt.Print(ansiFgGreen)
		// }
		// // TODO: instead, save totals and print at end
		// fmt.Printf("%sTOTAL %d -> %d%s\n", ansiBold, aTot, bTot, ansiReset)
	}
	sizes.flush("text size")
	fmt.Println()
	io.Copy(os.Stdout, sizesBuf)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cleanFuncName(name string) string {
	name = strings.TrimPrefix(name, `"".`)
	name = strings.TrimPrefix(name, `type.`)
	return name
}

func scanDashS(r io.Reader, sha []byte, c chan<- *pkgScanner) {
	// Lazy: attach a fake package to the end
	// to flush out the final package being processed.
	rr := io.MultiReader(r, strings.NewReader("\n# EOF\n"))
	scan := bufio.NewScanner(rr)
	var pkgscan *pkgScanner
	for scan.Scan() {
		// Look for "# pkgname".
		b := scan.Bytes()
		if len(b) == 0 {
			continue
		}
		if len(b) >= 2 && b[0] == '#' && b[1] == ' ' {
			// Found new package.
			// If we were working on a package, flush and emit it.
			if pkgscan != nil {
				pkgscan.flush()
				c <- pkgscan
			}
			pkgscan = &pkgScanner{
				Name:  string(b[2:]),
				Funcs: make(map[string]stextFunc),
				Hash:  sha256.New(),
			}
			continue
		}
		// Not a new package. Pass the line on to the current WIP package.
		if pkgscan != nil {
			// TODO: bytes.ReplaceAll allocates; modify in-place instead
			b = bytes.ReplaceAll(b, sha, []byte("SHA"))
			pkgscan.ProcessLine(b)
		}
	}
	check(scan.Err())
	close(c)
}

type pkgScanner struct {
	Name  string
	Funcs map[string]stextFunc
	// transient state
	Hash  hash.Hash
	stext string
}

func (s *pkgScanner) ProcessLine(b []byte) {
	if b[0] != '\t' {
		s.flush()
		s.stext = string(b)
		return
	}
	s.Hash.Write(b)
	s.Hash.Write([]byte{'\n'})
}

func (s *pkgScanner) flush() {
	if s.stext != "" && strings.Contains(s.stext, " STEXT ") {
		name, size := extractNameAndSize(s.stext)
		s.Funcs[name] = stextFunc{
			textsize: size,
			bodyhash: s.Hash.Sum(nil),
		}
	}
	s.Hash.Reset()
	s.stext = ""
}

type stextFunc struct {
	textsize int    // length in instructions of the function
	bodyhash []byte // hash of -S output for the function
}

func extractNameAndSize(stext string) (string, int) {
	orig := stext
	bail := func(msg string) (string, int) {
		log.Fatalf("malformed STEXT line: %s: %q", msg, orig)
		return "", 0
	}
	i := strings.IndexByte(stext, ' ')
	if i < 0 {
		return bail("no space after name")
	}
	name := stext[:i]
	stext = stext[i:]
	i = strings.Index(stext, " size=")
	if i < 0 {
		return bail("no size= field")
	}
	stext = stext[i+len(" size="):]
	i = strings.Index(stext, " ")
	if i < 0 {
		return bail("no space after size=")
	}
	stext = stext[:i]
	n, err := strconv.Atoi(stext)
	if err != nil {
		return bail("size= not an integer")
	}
	return name, n
}

func streamDashS(platform string, c commit) (wait func(), r io.Reader) {
	cmdgo := filepath.Join(c.dir, "bin", "go")
	cmd := exec.Command(cmdgo, "build", "-gcflags=all=-S -dwarf=false", "std", "cmd")
	goos, goarch := parsePlatform(platform)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
	pipe, err := cmd.StderrPipe()
	check(err)
	err = cmd.Start()
	check(err)
	wait = func() {
		err := cmd.Wait()
		check(err)
	}
	return wait, pipe
}
