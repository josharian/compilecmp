package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

const debug = false // print commands as they are run

var (
	flagRun     = flag.String("run", "", "run benchmarks matching regex")
	flagAll     = flag.Bool("all", false, "run all benchmarks, not just short ones")
	flagCPU     = flag.Bool("cpu", false, "run only CPU tests, not alloc tests")
	flagObj     = flag.Bool("obj", false, "report object file sizes")
	flagPkg     = flag.String("pkg", "", "benchmark compilation of `pkg`")
	flagCount   = flag.Int("n", 0, "iterations")
	flagEach    = flag.Bool("each", false, "run for every commit between before and after")
	flagCL      = flag.Int("cl", 0, "run benchmark on CL number")
	flagFn      = flag.String("fn", "", "find changed functions: all, changed, smaller, bigger, stats, or help")
	flagDumpSSA = flag.String("dumpssa", "", "dump SSA html for named functions (use like GOSSAFUNC)")
	flagAllBash = flag.Bool("allbash", false, "run all.bash for each commit")

	flagFlags       = flag.String("flags", "", "compiler flags for both before and after")
	flagBeforeFlags = flag.String("beforeflags", "", "compiler flags for before")
	flagAfterFlags  = flag.String("afterflags", "", "compiler flags for after")
	flagPlatforms   = flag.String("platforms", "", "comma-separated list of platforms to compile for; all=all platforms, arch=one platform per arch")
)

var cwd string

func main() {
	flag.Parse()
	log.SetFlags(0)

	cleanCache()

	// Make a temp dir to use for the GOCACHE.
	// See golang.org/issue/29561.
	dir, err := ioutil.TempDir("", "compilecmp-gocache-")
	check(err)
	if debug {
		fmt.Printf("GOCACHE=%s\n", dir)
	}
	defer os.RemoveAll(dir)
	os.Setenv("GOCACHE", dir)

	cwd, err = os.Getwd()
	if err != nil {
		log.Fatalf("could not get current working dir: %v", err)
	}
	beforeRef := "master"
	afterRef := "HEAD"
	if *flagCL != 0 {
		if flag.NArg() > 0 {
			log.Fatal("-cl NNN is incompatible with ref arguments")
		}
		clHead, parent, err := clHeadAndParent(*flagCL)
		if err != nil {
			log.Fatalf("failed to get CL %d information: %v", *flagCL, err)
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
	// Resolve immediately, for two reasons:
	// catch ref problems early,
	// and lock in stone the resolution in case the user changes branches
	resolve(beforeRef)
	resolve(afterRef)

	switch *flagFn {
	case "", "all", "changed", "smaller", "bigger", "stats":
	case "help":
		fallthrough
	default:
		fmt.Fprintln(os.Stdout, `
all: print all functions whose contents have changed, regardless of whether their text size changed
changed: print only functions whose text size has changed
smaller: print only functions whose text size has gotten smaller
bigger: print only functions whose text size has gotten bigger
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

	compare(beforeRef, afterRef)
	if !*flagEach {
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
		fmt.Println("---")
		compare(before, after)
	}
}

func combineFlags(x, y string) string {
	x = strings.TrimSpace(x)
	y = strings.TrimSpace(y)
	switch {
	case x == "":
		return y
	case y == "":
		return x
	}
	return x + " " + y
}

func printcommit(ref string) {
	sha := resolve(ref)
	if !strings.HasPrefix(ref, sha) {
		fmt.Printf("%s (%s): %s\n", ref, sha, commitmessage(sha))
	} else {
		// TODO: try rev-parse to get a "pretty" name for this ref.
		fmt.Printf("%s: %s\n", sha, commitmessage(sha))
	}
}

func allPlatforms() []string {
	cmd := exec.Command("go", "tool", "dist", "list")
	out, err := cmd.CombinedOutput()
	// TODO: maybe I should run this in the before/after
	// repos, since I know they should have functional tools...
	if err != nil {
		log.Fatalf("failed to run 'go tool dist list': %v", err)
	}
	out = bytes.TrimSpace(out)
	return strings.Split(string(out), "\n")
}

func compare(beforeRef, afterRef string) {
	var platforms []string
	switch *flagPlatforms {
	case "all":
		platforms = allPlatforms()
	case "arch":
		// one platform per architecture
		// in practice, right now, this means linux/* and js/wasm
		all := allPlatforms()
		for _, platform := range all {
			goos, goarch := parsePlatform(platform)
			if goos == "linux" || goarch == "wasm" {
				platforms = append(platforms, platform)
			}
		}
	default:
		platforms = strings.Split(*flagPlatforms, ",")
	}
	for _, platform := range platforms {
		comparePlatform(platform, beforeRef, afterRef)
	}
}

func comparePlatform(platform, beforeRef, afterRef string) {
	fmt.Printf("compilecmp %s -> %s\n", beforeRef, afterRef)
	printcommit(beforeRef)
	printcommit(afterRef)

	if platform != "" {
		fmt.Printf("platform: %s\n", platform)
	}

	beforeFlags := combineFlags(*flagFlags, *flagBeforeFlags)
	if beforeFlags != "" {
		fmt.Printf("before flags: %s\n", beforeFlags)
	}
	afterFlags := combineFlags(*flagFlags, *flagAfterFlags)
	if afterFlags != "" {
		fmt.Printf("after flags: %s\n", afterFlags)
	}

	before := worktree(beforeRef)
	after := worktree(afterRef)
	if debug {
		fmt.Printf("before GOROOT: %s\n", before.dir)
		fmt.Printf("after GOROOT: %s\n", after.dir)
	}

	if *flagCount > 0 {
		fmt.Println()
		fmt.Println("benchstat", before.tmp.Name(), after.tmp.Name())
		e := ETA{start: time.Now(), n: *flagCount}
		e.update(0)
		for i := 0; i < *flagCount+1; i++ {
			record := i != 0 // don't record the first run
			if record {
				e.update(i - 1)
			}
			before.bench(platform, beforeFlags, record, after.dir)
			after.bench(platform, afterFlags, record, after.dir)
			if record {
				e.update(i)
			}
		}
		fmt.Println()
	}
	check(before.tmp.Close())
	check(after.tmp.Close())
	if *flagCount > 0 {
		cmd := exec.Command("benchstat", before.tmp.Name(), after.tmp.Name())
		out, err := cmd.CombinedOutput()
		check(err)
		fmt.Println(string(out))
		fmt.Println()
	}
	fmt.Println()
	if platform != "" {
		before.cmdgo(platform, "install", "std", "cmd")
		after.cmdgo(platform, "install", "std", "cmd")
	}
	compareBinaries(platform, before, after)
	fmt.Println()
	if *flagObj {
		compareObjectFiles(platform, before, after)
		fmt.Println()
	}
	if *flagFn != "" {
		compareFunctions(platform, before, after)
		fmt.Println()
	}
	if *flagDumpSSA != "" {
		dumpSSA(platform, before, after, *flagDumpSSA)
	}
	// todo: notification?

	// Clean the go cache; see golang.org/issue/29561.
	after.cmdgo("", "clean", "-cache")
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

func compareBinaries(platform string, before, after commit) {
	sizes := newFilesizes(os.Stdout)
	// TODO: use glob instead of hard-coding
	goos, goarch := parsePlatform(platform)
	dirs := []string{"pkg/tool/" + goos + "_" + goarch}
	if platform != "" {
		dirs = append(dirs, "bin")
	}
	for _, dir := range dirs {
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

func compareObjectFiles(platform string, before, after commit) {
	platformPath := strings.ReplaceAll(platform, "/", "_")
	pkg := filepath.Join(before.dir, "pkg")
	if platformPath != "" {
		pkg = filepath.Join(pkg, platformPath)
	}
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
		afterPath := filepath.Join(after.dir, "pkg")
		if platformPath != "" {
			afterPath = filepath.Join(afterPath, platformPath)
		}
		afterPath = filepath.Join(afterPath, suff)
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
		log.Panic(err)
	}
}

func parsePlatform(platform string) (goos, goarch string) {
	if platform == "" {
		return runtime.GOOS, runtime.GOARCH
	}
	f := strings.Split(platform, "/")
	if len(f) != 2 {
		panic("bad platform: " + platform)
	}
	return f[0], f[1]
}

type commit struct {
	ref string
	sha string
	dir string
	tmp *os.File
}

func (c *commit) cmdgo(platform string, args ...string) []byte {
	cmdgo := filepath.Join(c.dir, "bin", "go")
	cmd := exec.Command(cmdgo, args...)
	goos, goarch := parsePlatform(platform)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
	cmd.Dir = filepath.Join(c.dir, "src")
	out, err := cmd.CombinedOutput()
	check(err)
	return out
}

func (c *commit) bench(platform, compilerflags string, record bool, goroot string) {
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
	goos, goarch := parsePlatform(platform)
	cmd.Env = append(os.Environ(), path, "GOOS="+goos, "GOARCH="+goarch)
	cmd.Dir = c.dir
	if record {
		cmd.Stdout = c.tmp
	}
	err := cmd.Run()
	check(err)
}

func git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	return bytes.TrimSpace(out), err
}

var (
	resolveMu sync.Mutex
	resolved  = map[string]string{} // ref -> sha
)

func resolve(ref string) string {
	resolveMu.Lock()
	defer resolveMu.Unlock()
	if sha, ok := resolved[ref]; ok {
		return sha
	}
	// Resolve ref to a sha1.
	out, err := git("rev-parse", "--short", ref)
	if err != nil {
		log.Fatalf("could not resolve ref %q: %v", ref, err)
	}
	sha := string(out)
	resolved[ref] = sha
	return sha
}

func worktree(ref string) commit {
	u, err := user.Current()
	check(err)
	sha := resolve(ref)
	dest := filepath.Join(u.HomeDir, ".compilecmp", sha)
	if !exists(dest) {
		if debug {
			fmt.Printf("cp <%s> %s\n", ref, dest)
		}
		if _, err := git("worktree", "add", "--detach", dest, ref); err != nil {
			log.Fatalf("could not create worktree for %q (%q): %v", ref, sha, err)
		}
	}
	var commands []string
	cmdgo := filepath.Join(dest, "bin", "go")
	switch {
	case *flagAllBash:
		// If requested, run all.bash.
		commands = append(commands, filepath.Join(dest, "src", "all.bash"))
	case exists(cmdgo):
		// cmd/go exists, presumably from a previous run.
		// Make sure everything is built, just in case a prior make.bash got interrupted.
		commands = append(commands, cmdgo+" install std cmd")
	default:
		// No cmd/go. Probably a new installation. Run make.bash.
		commands = append(commands, filepath.Join(dest, "src", "make.bash"))
	}
	for _, command := range commands {
		args := strings.Split(command, " ")
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = filepath.Join(dest, "src")
		if debug {
			fmt.Println(command)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Fatalf("%s\n%v", out, err)
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

func cleanCache() {
	u, err := user.Current()
	check(err)
	root := filepath.Join(u.HomeDir, ".compilecmp")
	err = os.MkdirAll(root, 0755)
	check(err)
	f, err := os.Open(root)
	check(err)
	defer f.Close()
	fis, err := f.Readdir(-1)
	check(err)

	// Look through ~/.compilecmp for any shas
	// that are no longer contained in any branch, and delete them.
	// This is the most common way to end up accumulating
	// lots of junk in .compilecmp.
	var wg sync.WaitGroup
	gate := make(chan bool, 10) // gate concurrent calls
	for _, fi := range fis {
		if !fi.IsDir() {
			continue
		}
		wg.Add(1)
		go func(sha string) {
			defer wg.Done()
			gate <- true
			defer func() { <-gate }()
			wt := filepath.Join(root, sha)
			cmd := exec.Command("git", "branch", "--contains", sha)
			cmd.Dir = wt
			out, err := cmd.CombinedOutput()
			okToDelete := false
			if err != nil {
				if strings.Contains(string(out), "not a git repository") {
					// partially initialized repo; nuke it
					okToDelete = true
				} else {
					log.Fatalf("%s\n%s$ %s: %v", out, wt, cmd, err)
				}
			}
			s := strings.TrimSpace(string(out))
			lines := strings.Split(s, "\n")
			if okToDelete || len(lines) == 0 ||
				(len(lines) == 1 && lines[0] == "* (no branch)") {
				// OK to delete
				err := os.RemoveAll(wt)
				if err != nil {
					log.Printf("failed to remove unreachable worktree %s: %v", wt, err)
				}
			}
		}(fi.Name())
	}
	wg.Wait()

	// TODO: also look for very old versions?
	// We could do this by always touching (say) GOROOT/VERSION
	// every time we use a worktree, and then looking at last mtime.
}

type ETA struct {
	start time.Time
	n     int
}

func (e *ETA) update(i int) {
	elapsed := time.Since(e.start)
	eta := "??"
	remain := "??"
	if i > 0 {
		avg := elapsed / time.Duration(i)
		r := (time.Duration(e.n - i)) * avg
		r /= time.Second
		r *= time.Second
		remain = fmt.Sprint(r)
		eta = time.Now().Add(r).Round(time.Second).Format(time.Kitchen)
	}
	digits := int(math.Ceil(math.Log10(float64(e.n + 1))))
	fmt.Printf("\rcompleted %[1]*d of %d, estimated time remaining %v (ETA %v)      ", digits, i, e.n, remain, eta)
}
