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
		args := []string{"build"}
		if pkg != "" {
			args = append(args, pkg)
		} else {
			args = append(args, "std", "cmd")
		}
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
		for scan.Scan() {
			s := scan.Text()
			if len(s) == 0 {
				continue
			}
			const dumpedSSATo = "dumped SSA to "
			if strings.HasPrefix(s, dumpedSSATo) {
				relpath := s[len(dumpedSSATo):]
				if !strings.HasSuffix(relpath, "ssa.html") {
					panic("wrote ssa to non-ssa.html file")
				}
				src := filepath.Join(c.dir, "src", relpath)
				prefix := ""
				if platform != "" {
					prefix = fmt.Sprintf("%s_%s_", goos, goarch)
				}
				dst := filepath.Join(c.dir, "src", strings.TrimSuffix(relpath, "ssa.html")+prefix+filename+".html")
				err = os.Rename(src, dst)
				check(err)
				fmt.Println(dst)
			}
		}
		check(scan.Err())
	}
	fmt.Println()
}

func splitPkgFnname(in string) (pkg, fnname string) {
	fnname = in
	if slash := strings.LastIndex(fnname, "/"); slash >= 0 {
		pkg = fnname[:slash]
		fnname = fnname[slash:]
	}
	if !strings.ContainsAny(fnname, "()*") {
		if dot := strings.Index(fnname, "."); dot >= 0 {
			pkg += fnname[:dot]
			fnname = fnname[dot+1:]
		}
	}
	return pkg, fnname
}
