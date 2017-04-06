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
	"strings"
	"time"
)

var (
	flagRun   = flag.String("run", "", "run benchmarks matching regex")
	flagAll   = flag.Bool("all", false, "run all benchmarks, not just short ones")
	flagCPU   = flag.Bool("cpu", false, "run only CPU tests, not alloc tests")
	flagCount = flag.Int("n", 15, "iterations")
	flag386   = flag.Bool("386", false, "run in 386 mode")
	flagEach  = flag.Bool("each", false, "run for every commit between before and after")

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
	fmt.Printf("compilecmp %s %s %s %s\n", *flagBeforeFlags, beforeRef, *flagAfterFlags, afterRef)
	before := worktree(beforeRef)
	after := worktree(afterRef)
	fmt.Println("before:", before.dir)
	fmt.Println("after: ", after.dir)
	fmt.Println("benchstat", before.tmp.Name(), after.tmp.Name())
	start := time.Now()
	for i := 0; i < *flagCount+1; i++ {
		record := i != 0 // don't record the first run
		before.bench(*flagBeforeFlags, record)
		after.bench(*flagAfterFlags, record)
		elapsed := time.Since(start)
		avg := elapsed / time.Duration(i+1)
		remain := (time.Duration(*flagCount - i)) * avg
		remain /= time.Second
		remain *= time.Second
		fmt.Printf("\rcompleted % 4d of % 4d, estimated time remaining %v (eta %v)      ", i, *flagCount, remain, time.Now().Add(remain).Round(time.Second).Format(time.Kitchen))
	}
	fmt.Println()
	check(before.tmp.Close())
	check(after.tmp.Close())
	cmd := exec.Command("benchstat", before.tmp.Name(), after.tmp.Name())
	out, err := cmd.CombinedOutput()
	check(err)
	fmt.Println(string(out))
	// todo: notification
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

func (c *commit) bench(compilerflags string, record bool) {
	var args []string
	if !*flagAll {
		args = append(args, "-short")
	}
	if !*flagCPU {
		args = append(args, "-alloc")
	}
	if *flagRun != "" {
		args = append(args, "-run", *flagRun)
	}
	if compilerflags != "" {
		args = append(args, "-compileflags", compilerflags)
	}
	cmd := exec.Command("compilebench", args...)
	path := "PATH=" + filepath.Join(c.dir, "bin")
	if sz, err := exec.LookPath("size"); err == nil {
		path += ":" + filepath.Dir(sz)
	}
	cmd.Env = []string{path}
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
		log.Printf("copy tree at %s (%s) to %s", ref, sha, dest)
		if _, err := git("worktree", "add", "--detach", dest, ref); err != nil {
			log.Fatalf("could not create worktree for %q (%q): %v", ref, sha, err)
		}
	}
	cmdgo := filepath.Join(dest, "bin", "go")
	if !exists(cmdgo) {
		cmd := exec.Command("./make.bash")
		if *flag386 {
			cmd.Env = append(os.Environ(), "GOARCH=386", "GOHOSTARCH=386")
		}
		cmd.Dir = filepath.Join(dest, "src")
		log.Printf("%s/make.bash", cmd.Dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Fatalf("%s", out)
		}
	}
	tmp, err := ioutil.TempFile("", "")
	check(err)
	return commit{ref: ref, sha: sha, dir: dest, tmp: tmp}
}

func exists(path string) bool {
	// can stat? it exists. good enough.
	_, err := os.Stat(path)
	return err == nil
}
