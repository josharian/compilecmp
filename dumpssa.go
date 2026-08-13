package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func dumpSSA(platform string, before, after commit, fnname string) {
	fmt.Printf("dumping SSA for %v:\n", fnname)
	// split fnname into pkg+fnname, if necessary
	pkg, fnname := splitPkgFnname(fnname)
	if pkg == "" {
		log.Fatalf("must specify package for %v", fnname)
	}

	// make fnname into an easier to deal with filename
	filename := strings.ReplaceAll(fnname, "(", "_")
	filename = strings.ReplaceAll(filename, ")", "_")
	filename = strings.ReplaceAll(filename, ":", "_")
	filename = strings.ReplaceAll(filename, "*", ".")
	filename = strings.ReplaceAll(filename, "\"", "_")
	filename = strings.ReplaceAll(filename, "[", "_")
	filename = strings.ReplaceAll(filename, "]", "_")

	for _, c := range []commit{before, after} {
		cmdgo := filepath.Join(c.dir, "bin", "go")
		args := []string{"build", "-a", pkg}
		cmd := exec.Command(cmdgo, args...)
		goos, goarch := parsePlatform(platform)
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "GOSSAFUNC="+fnname)
		cmd.Dir = filepath.Join(c.dir, "src")
		out, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf("%v:\n%s\n", cmd, out)
			log.Fatal(err)
		}

		scan := bufio.NewScanner(bytes.NewReader(out))
		dumped := false
		for scan.Scan() {
			s := scan.Text()
			if len(s) == 0 {
				continue
			}
			if path, ok := ssaDumpPath(s); ok {
				if !strings.HasSuffix(path, "ssa.html") {
					panic("wrote ssa to non-ssa.html file")
				}
				if !filepath.IsAbs(path) {
					path = filepath.Join(c.dir, "src", path)
				}
				src := path
				prefix := ""
				if platform != "" {
					prefix = fmt.Sprintf("%s_%s_", goos, goarch)
				}
				dst := strings.TrimSuffix(path, "ssa.html") + prefix + filename + ".html"
				err = os.Rename(src, dst)
				check(err)
				fmt.Println(dst)
				dumped = true
			}
		}
		check(scan.Err())
		if !dumped {
			log.Fatalf("compiler did not dump SSA for %s at %s", fnname, c.ref)
		}
	}
	fmt.Println()
}

func ssaDumpPath(line string) (string, bool) {
	const oldPrefix = "dumped SSA to "
	if strings.HasPrefix(line, oldPrefix) {
		return line[len(oldPrefix):], true
	}
	if strings.HasPrefix(line, "dumped SSA for ") {
		if i := strings.LastIndex(line, " to "); i >= 0 {
			return line[i+len(" to "):], true
		}
	}
	return "", false
}

func splitPkgFnname(in string) (pkg, fnname string) {
	tail := in
	offset := 0
	if slash := strings.LastIndex(in, "/"); slash >= 0 {
		offset = slash + 1
		tail = in[offset:]
	}
	if strings.HasPrefix(tail, "(") {
		return "", in
	}
	if dot := strings.IndexByte(tail, '.'); dot >= 0 {
		i := offset + dot
		return in[:i], in[i+1:]
	}
	return "", in
}
