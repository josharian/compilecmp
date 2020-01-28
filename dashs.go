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
	"strconv"
	"strings"
)

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
	for {
		aPkg := <-aChan
		bPkg := <-bChan
		if aPkg == nil && bPkg == nil {
			// all done
			break
		}
		switch {
		case aPkg == nil:
			log.Fatalf("-fn does not yet handle added/deleted packages, but found an added package %s", bPkg.Name)
		case bPkg == nil:
			log.Fatalf("-fn does not yet handle added/deleted packages, but found a deleted package %s", aPkg.Name)
		case aPkg.Name != bPkg.Name:
			log.Fatalf("-fn does not yet handle added/deleted packages, got %s != %s", aPkg.Name, bPkg.Name)
		}
		pkg := aPkg.Name

		needsHeader := true
		printHeader := func() {
			if !needsHeader {
				return
			}
			fmt.Printf("\n%s%s%s%s\n", ansiFgYellow, ansiBold, pkg, ansiReset)
			needsHeader = false
		}

		var aTot, bTot int
		for name, asf := range aPkg.Funcs {
			aTot += asf.textsize
			bsf, ok := bPkg.Funcs[name]
			if !ok {
				if *flagFn != "stats" {
					printHeader()
					fmt.Println("DELETED", name)
				}
				continue
			}
			delete(bPkg.Funcs, name)
			bTot += bsf.textsize
			if bytes.Equal(asf.bodyhash, bsf.bodyhash) {
				continue
			}
			// TODO: option to show these
			if asf.textsize == bsf.textsize {
				if *flagFn == "all" {
					printHeader()
					fmt.Print(ansiFgBlue)
					fmt.Println(name, "changed")
					fmt.Print(ansiReset)
				}
				// TODO: option for this?
				// diff.Text("a", "b", asf.body, bsf.body, os.Stdout)
				continue
			}
			color := ""
			show := true
			if asf.textsize < bsf.textsize {
				if *flagFn == "better" || *flagFn == "stats" {
					show = false
				}
				color = ansiFgRed
			} else {
				if *flagFn == "worse" || *flagFn == "stats" {
					show = false
				}
				color = ansiFgGreen
			}
			if show {
				printHeader()
				fmt.Print(color)
				fmt.Println(strings.TrimPrefix(name, `"".`), asf.textsize, "->", bsf.textsize)
				fmt.Print(ansiReset)
			}
		}
		for name, bsf := range bPkg.Funcs {
			if *flagFn != "stats" {
				printHeader()
				fmt.Println("INSERTED", name)
			}
			bTot += bsf.textsize
		}
		sizes.add(string(pkg)[len("# "):]+".s", int64(aTot), int64(bTot))
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
		if b[0] == '#' && b[1] == ' ' {
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
	body     string
}

func extractNameAndSize(stext string) (string, int) {
	i := strings.IndexByte(stext, ' ')
	name := stext[:i]
	stext = stext[i:]
	i = strings.Index(stext, " size=")
	stext = stext[i+len(" size="):]
	i = strings.Index(stext, " ")
	stext = stext[:i]
	n, err := strconv.Atoi(stext)
	check(err)
	return name, n
}

func streamDashS(platform string, c commit) (wait func(), r io.Reader) {
	cmdgo := filepath.Join(c.dir, "bin", "go")
	cmd := exec.Command(cmdgo, "build", "-p=1", "-gcflags=all=-S -dwarf=false", "std", "cmd")
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
