package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func dumpSSA(platform string, before, after commit, fnname string) {
	fmt.Printf("dumping SSA for %v:\n", fnname)
	// split fnname into pkg+fnname, if necessary
	var pkg string
	if slash := strings.LastIndex(fnname, "/"); slash >= 0 {
		pkg = fnname[:slash]
		fnname = fnname[slash:]
	}
	if dot := strings.Index(fnname, "."); dot >= 0 {
		pkg += fnname[:dot]
		fnname = fnname[dot+1:]
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
		pipe, err := cmd.StderrPipe()
		check(err)
		// Duplicate output to a buffer, in case there is an error.
		buf := new(bytes.Buffer)
		tee := io.TeeReader(pipe, buf)
		err = cmd.Start()
		check(err)

		scan := bufio.NewScanner(tee)
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

		if err := cmd.Wait(); err != nil {
			fmt.Println(buf.String())
			log.Fatal(err)
		}
	}
	fmt.Println()
}
