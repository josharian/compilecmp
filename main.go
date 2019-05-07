package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
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
	flag386   = flag.Bool("386", false, "run in 386 mode")
	flagEach  = flag.Bool("each", false, "run for every commit between before and after")

	flagFlags       = flag.String("flags", "", "compiler flags for both before and after")
	flagBeforeFlags = flag.String("beforeflags", "", "compiler flags for before")
	flagAfterFlags  = flag.String("afterflags", "", "compiler flags for after")
)

var cwd string

func main() {
	log.SetFlags(log.Ltime)
	var err error
	cwd, err = os.Getwd()
	if err != nil {
		log.Fatalf("could not working dir: %v", err)
	}
	flag.Parse()
	beforeRef := "master"
	afterRef := "HEAD"
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
	// TODO: compare all object files
	fmt.Println()
	// todo: notification
}

func compareBinaries(before, after commit) {
	w := tabwriter.NewWriter(os.Stdout, 8, 8, 1, ' ', 0)
	fmt.Fprintln(w, "file\tbefore\tafter\tΔ\t%\t")
	var totbefore, totafter int64
	haschange := false
	// TODO: use glob instead of hard-coding
	for _, dir := range []string{"bin", "pkg/tool/" + runtime.GOOS + "_" + runtime.GOARCH} {
		for _, base := range []string{"go", "addr2line", "api", "asm", "buildid", "cgo", "compile", "cover", "dist", "doc", "fix", "link", "nm", "objdump", "pack", "pprof", "test2json", "trace", "vet"} {
			path := filepath.FromSlash(dir + "/" + base)
			before := filesize(filepath.Join(before.dir, path))
			after := filesize(filepath.Join(after.dir, filepath.FromSlash(path)))
			if before == 0 || after == 0 {
				continue
			}
			totbefore += before
			totafter += after
			if before == after {
				continue
			}
			haschange = true
			name := filepath.Base(path)
			fmt.Fprintf(w, "%s\t%d\t%d\t%+d\t%+0.3f%%\t\n", name, before, after, after-before, 100*float64(after)/float64(before)-100)
		}
	}
	if haschange {
		fmt.Fprintf(w, "%s\t%d\t%d\t%+d\t%+0.3f%%\t\n", "total", totbefore, totafter, totafter-totbefore, 100*float64(totafter)/float64(totbefore)-100)
		w.Flush()
	} else {
		fmt.Println("no binary size changes")
	}
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
	out, err := git("rev-parse", ref)
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
	if *flag386 {
		dest += "-386"
	}
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
		if *flag386 {
			cmd.Env = append(os.Environ(), "GOARCH=386", "GOHOSTARCH=386")
		}
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
