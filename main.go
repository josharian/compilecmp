package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

var (
	flagRun   = flag.String("run", "", "run benchmarks matching regex")
	flagAll   = flag.Bool("all", false, "run all benchmarks, not just short ones")
	flagCPU   = flag.Bool("cpu", false, "run only CPU tests, not alloc tests")
	flagObj   = flag.Bool("obj", false, "report object file sizes")
	flagPkg   = flag.String("pkg", "", "benchmark compilation of `pkg`")
	flagCount = flag.Int("n", 15, "iterations")
	flagEach  = flag.Bool("each", false, "run for every commit between before and after")
	flagCL    = flag.Int("cl", 0, "run benchmark on CL number")
	flagFn    = flag.String("fn", "", "find changed functions: all, changed, better, worse, stats, or help")

	flagFlags       = flag.String("flags", "", "compiler flags for both before and after")
	flagBeforeFlags = flag.String("beforeflags", "", "compiler flags for before")
	flagAfterFlags  = flag.String("afterflags", "", "compiler flags for after")
)

var cwd string

func main() {
	// todo: limit to n old compilecmp directories, maybe sort by atime? mtime?
	flag.Parse()

	log.SetFlags(log.Ltime)
	var err error
	cwd, err = os.Getwd()
	if err != nil {
		log.Fatalf("could not working dir: %v", err)
	}
	beforeRef := "master"
	afterRef := "HEAD"
	if *flagCL != 0 {
		if flag.NArg() > 0 {
			log.Fatal("-cl NNN is incompatible with ref arguments")
		}
		clHead, parent, err := clHeadAndParent(*flagCL)
		if err != nil {
			log.Fatalf("failed to get CL %s information: %v", *flagCL, err)
		}
		if parent == "" {
			log.Fatal("CL does not have parent")
		}
		beforeRef = parent
		afterRef = clHead
	}
	switch flag.NArg() {
	case 0:
	case 1:
		beforeRef = flag.Arg(0)
	case 2:
		beforeRef = flag.Arg(0)
		afterRef = flag.Arg(1)
	default:
		log.Fatal("usage: compilecmp [before-git-ref] [after-git-ref]")
	}

	switch *flagFn {
	case "", "all", "changed", "better", "worse", "stats":
	case "help":
		fallthrough
	default:
		fmt.Fprintln(os.Stdout, `
all: print all functions whose contents have changed, regardless of whether their text size changed
changed: print only functions whose text size has changed
better: print only functions whose text size has gotten smaller
worse: print only functions whose text size has gotten bigger
stats: print only the summary (per package function size total)
help: print this message and exit
`[1:])
		os.Exit(2)
	}

	// Clean up unused worktrees to avoid error under the following circumstances:
	// * run compilecmp ref1 ref2
	// * rm -r ~/.compilecmp
	// * run compilecmp ref1 ref2
	// git gets confused because it thinks ref1 and ref2 have worktrees.
	// Pruning fixes that.
	if _, err := git("worktree", "prune"); err != nil {
		log.Fatalf("could not prune worktrees: %v", err)
	}

	if !*flagEach {
		compare(beforeRef, afterRef)
		return
	}
	list, err := git("rev-list", afterRef, beforeRef+".."+afterRef)
	check(err)
	revs := strings.Fields(string(list))
	for i := len(revs); i > 0; i-- {
		before := beforeRef
		if i < len(revs) {
			before = revs[i]
		}
		after := revs[i-1]
		compare(before, after)
	}
}

func compare(beforeRef, afterRef string) {
	beforeFlags := *flagFlags + " " + *flagBeforeFlags
	afterFlags := *flagFlags + " " + *flagAfterFlags
	log.Printf("compilecmp %s %s %s %s", beforeFlags, beforeRef, afterFlags, afterRef)
	log.Printf("%s: %s", beforeRef, commitmessage(beforeRef))
	log.Printf("%s: %s", afterRef, commitmessage(afterRef))
	before := worktree(beforeRef)
	after := worktree(afterRef)
	log.Printf("before: %s", before.dir)
	log.Printf("after: %s", after.dir)
	if *flagCount > 0 {
		fmt.Println()
		fmt.Println("benchstat -geomean ", before.tmp.Name(), after.tmp.Name())
		start := time.Now()
		for i := 0; i < *flagCount+1; i++ {
			record := i != 0 // don't record the first run
			before.bench(beforeFlags, record, after.dir)
			after.bench(afterFlags, record, after.dir)
			elapsed := time.Since(start)
			avg := elapsed / time.Duration(i+1)
			remain := (time.Duration(*flagCount - i)) * avg
			remain /= time.Second
			remain *= time.Second
			fmt.Printf("\rcompleted % 4d of % 4d, estimated time remaining %v (eta %v)      ", i, *flagCount, remain, time.Now().Add(remain).Round(time.Second).Format(time.Kitchen))
		}
		fmt.Println()
	}
	check(before.tmp.Close())
	check(after.tmp.Close())
	if *flagCount > 0 {
		cmd := exec.Command("benchstat", "-geomean", before.tmp.Name(), after.tmp.Name())
		out, err := cmd.CombinedOutput()
		check(err)
		fmt.Println(string(out))
		fmt.Println()
	}
	fmt.Println()
	compareBinaries(before, after)
	fmt.Println()
	if *flagObj {
		compareObjectFiles(before, after)
		fmt.Println()
	}
	if *flagFn != "" {
		compareFunctions(before, after)
		fmt.Println()
	}
	// todo: notification
}

func compareFunctions(before, after commit) {
	await, ascan := streamDashS(before)
	bwait, bscan := streamDashS(after)
	compareFuncScanners(ascan, bscan)
	await()
	bwait()
}

const (
	ansiBold     = "\u001b[1m"
	ansiDim      = "\u001b[2m"
	ansiFgRed    = "\u001b[31m"
	ansiFgGreen  = "\u001b[32m"
	ansiFgYellow = "\u001b[33m"
	ansiFgBlue   = "\u001b[36m"
	ansiFgWhite  = "\u001b[37m"
	ansiReset    = "\u001b[0m"
)

func compareFuncScanners(a, b *bufio.Scanner) {
	sizesBuf := new(bytes.Buffer)
	sizes := newFilesizes(sizesBuf)
	for a.Scan() && b.Scan() {
		aidx := bytes.IndexByte(a.Bytes(), '\n')
		bidx := bytes.IndexByte(b.Bytes(), '\n')
		pkg := a.Bytes()[:aidx]
		pkg2 := b.Bytes()[:bidx]
		if !bytes.Equal(pkg, pkg2) {
			log.Fatalf("-fn does not yet handle added/deleted packages, got %s != %s", pkg, pkg2)
		}
		// skip identical packages
		if bytes.Equal(a.Bytes(), b.Bytes()) {
			continue
		}
		needsHeader := true
		printHeader := func() {
			if !needsHeader {
				return
			}
			fmt.Printf("\n%s%s%s%s\n", ansiFgYellow, ansiBold, pkg, ansiReset)
			needsHeader = false
		}
		aPkg := parseDashSPackage(a.Bytes()[aidx:])
		bPkg := parseDashSPackage(b.Bytes()[bidx:])
		var aTot, bTot int
		for name, asf := range aPkg {
			aTot += asf.textsize
			bsf, ok := bPkg[name]
			if !ok {
				if *flagFn != "stats" {
					printHeader()
					fmt.Println("DELETED", name)
				}
				continue
			}
			delete(bPkg, name)
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
		for name, bsf := range bPkg {
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
	check(a.Err())
	check(b.Err())
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

var dashSInstructionDump = regexp.MustCompile("\t0x[0-9a-f]+( [0-9a-f]{2}){16}  ")

func parseDashSPackage(b []byte) map[string]stextFunc {
	m := make(map[string]stextFunc)
	scan := bufio.NewScanner(bytes.NewReader(b))
	h := sha256.New()
	var stext string
	var body []byte
	for scan.Scan() {
		if len(scan.Bytes()) == 0 || scan.Bytes()[0] != '\t' {
			if stext != "" && strings.Contains(stext, " STEXT ") {
				name, size := extractNameAndSize(stext)
				m[name] = stextFunc{
					textsize: size,
					bodyhash: h.Sum(nil),
					body:     string(body),
				}
			}
			h.Reset()
			body = nil
			stext = scan.Text()
			continue
		}
		h.Write(scan.Bytes())
		h.Write([]byte{'\n'})
		if dashSInstructionDump.Match(scan.Bytes()) {
			continue
		}
		if bytes.HasPrefix(scan.Bytes(), []byte("\trel ")) {
			continue
		}
		// TODO: resuscite if we want to support producing diffs
		// body = append(body, scan.Bytes()...)
		// body = append(body, '\n')
	}
	if stext != "" && strings.Contains(stext, "STEXT") {
		name, size := extractNameAndSize(stext)
		m[name] = stextFunc{
			textsize: size,
			bodyhash: h.Sum(nil),
			body:     string(body),
		}
	}
	check(scan.Err())
	return m
}

func scannerForDashS(r io.Reader, sha []byte) *bufio.Scanner {
	scanPackages := func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}

		// Look for "\n# pkgname\n".
		if i := bytes.Index(data, []byte("\n#")); i >= 0 {
			// # pkgname
			j := bytes.IndexByte(data[i:], '\n')
			if j < 0 {
				// need more data
				return 0, nil, nil
			}
			r := bytes.ReplaceAll(data[:i+j], sha, []byte("SHA"))
			return i + j + 1, r, nil
		}

		// If we're at EOF, return whatever is left.
		if atEOF {
			r := bytes.ReplaceAll(data, sha, []byte("SHA"))
			return len(data), r, nil
		}

		// Request more data.
		return 0, nil, nil
	}

	scan := bufio.NewScanner(r)
	scan.Split(scanPackages)
	scan.Buffer(nil, 1<<30)
	return scan
}

func streamDashS(c commit) (wait func(), scan *bufio.Scanner) {
	cmdgo := filepath.Join(c.dir, "bin", "go")
	cmd := exec.Command(cmdgo, "build", "-p=1", "-gcflags=all=-S -dwarf=false", "std", "cmd")
	pipe, err := cmd.StderrPipe()
	check(err)
	err = cmd.Start()
	check(err)
	wait = func() {
		err := cmd.Wait()
		check(err)
	}
	scan = scannerForDashS(pipe, []byte(c.sha))
	return
}

type filesizes struct {
	totbefore int64
	totafter  int64
	haschange bool
	out       io.Writer
	w         *tabwriter.Writer
}

func newFilesizes(out io.Writer) *filesizes {
	w := tabwriter.NewWriter(out, 8, 8, 1, ' ', 0)
	fmt.Fprintln(w, "file\tbefore\tafter\tΔ\t%\t")
	sizes := new(filesizes)
	sizes.w = w
	sizes.out = out
	return sizes
}

func (s *filesizes) add(name string, beforeSize, afterSize int64) {
	if beforeSize == 0 || afterSize == 0 {
		return
	}
	s.totbefore += beforeSize
	s.totafter += afterSize
	if beforeSize == afterSize {
		return
	}
	s.haschange = true
	fmt.Fprintf(s.w, "%s\t%d\t%d\t%+d\t%+0.3f%%\t\n", name, beforeSize, afterSize, afterSize-beforeSize, 100*float64(afterSize)/float64(beforeSize)-100)
}

func (s *filesizes) flush(desc string) {
	if s.haschange {
		fmt.Fprintf(s.w, "%s\t%d\t%d\t%+d\t%+0.3f%%\t\n", "total", s.totbefore, s.totafter, s.totafter-s.totbefore, 100*float64(s.totafter)/float64(s.totbefore)-100)
		s.w.Flush()
		return
	}
	fmt.Fprintf(s.out, "no %s size changes\n", desc)
}

func compareBinaries(before, after commit) {
	sizes := newFilesizes(os.Stdout)
	// TODO: use glob instead of hard-coding
	for _, dir := range []string{"bin", "pkg/tool/" + runtime.GOOS + "_" + runtime.GOARCH} {
		for _, base := range []string{"go", "addr2line", "api", "asm", "buildid", "cgo", "compile", "cover", "dist", "doc", "fix", "link", "nm", "objdump", "pack", "pprof", "test2json", "trace", "vet"} {
			path := filepath.FromSlash(dir + "/" + base)
			beforeSize := filesize(filepath.Join(before.dir, path))
			afterSize := filesize(filepath.Join(after.dir, filepath.FromSlash(path)))
			name := filepath.Base(path)
			sizes.add(name, beforeSize, afterSize)
		}
	}
	sizes.flush("binary")
}

func compareObjectFiles(before, after commit) {
	pkg := filepath.Join(before.dir, "pkg")
	var files []string
	err := filepath.Walk(pkg, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".a") || !strings.HasPrefix(path, pkg) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	check(err)
	sizes := newFilesizes(os.Stdout)
	for _, beforePath := range files {
		suff := beforePath[len(pkg):]
		afterPath := filepath.Join(after.dir, "pkg", suff)
		beforeSize := filesize(beforePath)
		afterSize := filesize(afterPath)
		// suff is of the form /arch/. Remove that.
		suff = filepath.ToSlash(suff)
		suff = suff[1:]                              // remove leading slash
		suff = suff[strings.IndexByte(suff, '/')+1:] // remove next slash
		sizes.add(suff, beforeSize, afterSize)
	}
	sizes.flush("object file")
}

func filesize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

type commit struct {
	ref string
	sha string
	dir string
	tmp *os.File
}

func (c *commit) bench(compilerflags string, record bool, goroot string) {
	var args []string
	if !*flagAll {
		args = append(args, "-short")
	}
	if !*flagCPU {
		args = append(args, "-alloc")
	}
	if *flagObj {
		args = append(args, "-obj")
	}
	if *flagPkg != "" {
		args = append(args, "-pkg", *flagPkg)
	}
	if *flagRun != "" {
		args = append(args, "-run", *flagRun)
	}
	if strings.TrimSpace(compilerflags) != "" {
		args = append(args, "-compileflags", compilerflags)
	}
	args = append(args, "-go="+filepath.Join(c.dir, "bin", "go"))
	cmd := exec.Command("compilebench", args...)
	path := "PATH=" + filepath.Join(c.dir, "bin")
	if sz, err := exec.LookPath("size"); err == nil {
		path += ":" + filepath.Dir(sz)
	}
	cmd.Env = append(os.Environ(), path /*, "GOROOT="+goroot*/)
	cmd.Dir = c.dir
	out, err := cmd.CombinedOutput()
	check(err)
	if record {
		_, err := c.tmp.Write(out)
		check(err)
	}
}

func git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	return bytes.TrimSpace(out), err
}

func resolve(ref string) string {
	// Resolve ref to a sha1.
	out, err := git("rev-parse", "--short", ref)
	if err != nil {
		log.Fatalf("could not resolve ref %q: %v", ref, err)
	}
	return string(out)
}

func worktree(ref string) commit {
	u, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	sha := resolve(ref)
	dest := filepath.Join(u.HomeDir, ".compilecmp", sha)
	if !exists(dest) {
		log.Printf("cp <%s> %s", ref, dest)
		if _, err := git("worktree", "add", "--detach", dest, ref); err != nil {
			log.Fatalf("could not create worktree for %q (%q): %v", ref, sha, err)
		}
	}
	var commands []string
	cmdgo := filepath.Join(dest, "bin", "go")
	if exists(cmdgo) {
		// Make sure everything is built, just in case a prior make.bash got interrupted.
		commands = append(commands, cmdgo+" install std cmd")
	} else {
		commands = append(commands, filepath.Join(dest, "src", "make.bash"))
	}
	for _, command := range commands {
		args := strings.Split(command, " ")
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = filepath.Join(dest, "src")
		log.Println(command)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Fatalf("%s", out)
		}
	}
	// These deletions are best effort.
	// See https://github.com/golang/go/issues/31851 for context.
	os.RemoveAll(filepath.Join(dest, "pkg", "obj"))
	os.RemoveAll(filepath.Join(dest, "pkg", "bootstrap"))
	tmp, err := ioutil.TempFile("", "")
	check(err)
	return commit{ref: ref, sha: sha, dir: dest, tmp: tmp}
}

func exists(path string) bool {
	// can stat? it exists. good enough.
	_, err := os.Stat(path)
	return err == nil
}

func commitmessage(ref string) []byte {
	b, err := git("log", "--format=%s", "-n", "1", ref)
	check(err)
	return b
}

// clHeadAndParent fetches the given CL to local, returns the CL HEAD and its parents commits.
func clHeadAndParent(cl int) (string, string, error) {
	clUrlFormat := "https://go-review.googlesource.com/changes/%d/?o=CURRENT_REVISION&o=ALL_COMMITS"
	resp, err := http.Get(fmt.Sprintf(clUrlFormat, cl))
	if err != nil {
		return "", "", err
	}

	// Work around https://code.google.com/p/gerrit/issues/detail?id=3540
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	body = bytes.TrimPrefix(body, []byte(")]}'"))

	var parse struct {
		CurrentRevision string `json:"current_revision"`
		Revisions       map[string]struct {
			Fetch struct {
				HTTP struct {
					URL string
					Ref string
				}
			}
			Commit struct {
				Parents []struct {
					Commit string
				}
			}
		}
	}

	if err := json.Unmarshal(body, &parse); err != nil {
		return "", "", err
	}
	parent := ""
	if len(parse.Revisions[parse.CurrentRevision].Commit.Parents) > 0 {
		parent = parse.Revisions[parse.CurrentRevision].Commit.Parents[0].Commit
	}

	ref := parse.Revisions[parse.CurrentRevision].Fetch.HTTP

	if _, err := git("fetch", ref.URL, ref.Ref); err != nil {
		return "", "", err
	}
	return parse.CurrentRevision, parent, nil
}
