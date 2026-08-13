compilecmp is a bit of a Swiss Army knife for Go compiler developers. It compares the compiler at different git commits. It can compare compilation time and memory usage, the sizes of the generated binaries, the sizes of the generated object files, and the generated code.

Run compilecmp from the Go repository containing the commits you want to
compare. Git refs are resolved in that repository.

Important caveats:

- compilecmp (unlike toolstash) uses the toolchain on itself, so if you (say) add a bunch of code to text/template, compilecmp will report that text/template got slower to compile; since the compiler itself is one of the subject packages, it can be ambiguous why a performance change for those entries occurred
- it is not safe to run multiple compilecmps concurrently
- on startup, compilecmp deletes cache entries not contained in any Git ref, which can be slow, because GOROOTs are large

# Specifying commits

compilecmp accepts up to two git refs as arguments.

- If no refs are provided, it assumes you are measuring from master to HEAD.

```
$ compilecmp  # compares master to head
```

- If one ref is provided, compilecmp treats it as the before commit.

```
$ compilecmp head~1  # compares current commit to its parent
```

- If two refs are provided, they are treated as the before and after commits.

```
$ compilecmp go1.13 speedy  # compares tagged go1.13 release to branch speedy
```

There is one exception: instead of providing any commits, you may provide -cl. This downloads a CL from gerrit and compares it to its parent.

```
$ compilecmp -cl 12345  # measures the performance impact of CL 12345
```

compilecmp can also easily measure a series of commits, usually in a branch, using `-each`.

```
$ compilecmp -each master head  # compares master to head, and then every individual commit between master and head to its parent
```

By default, compilecmp runs `make.bash` when creating a toolchain and uses
`go install std cmd` to refresh a cached toolchain. `-allbash` runs `all.bash`
for each commit instead.

# Number of runs

Some compiler outputs are always the same, like the generated code, object files, and binaries.

Others require multiple runs to measure accurately, such as allocations and CPU time. The `-n` flag lets you run multiple iterations. (Without `-n`, compilecmp does not report any data on allocations or CPU time.)

```
$ compilecmp -n 5  # run five iterations
```

`-n 5` is a good number for measuring allocations.

`-n 50` is a good number for measuring CPU time. `-n 100` is better, particularly if you are trying to detect small changes. This takes a long time! compilecmp will print an ETA. Be sure to quit all other running apps, including backups (like Time Machine). I also suggest using `-cpu` in this case, to skip allocation measurements, which yields more consistent results.

When `n > 0`, compilecmp also does a uncounted warmup run at the beginning.

By default, compilecmp skips long-running benchmarks. Use `-all` to include
them.

# Compare files sizes

By default, compilecmp prints the sizes of executables such as cmd/addr2line.

`-obj` adds object files sizes. Beware that object sizes aren’t always correlated to compilation quality! There’s lots of other stuff in there: dwarf, pclntab, export information, etc.

# Comparing generated code

compilecmp can also compare the generated code, function by function. (This part is still in flux a bit.)

- `-fn=all`: print all functions whose contents have changed
- `-fn=changed`: print all functions whose text size has changed
- `-fn=smaller`: print all functions whose text size has gotten smaller
- `-fn=bigger`: print all functions whose text size has gotten bigger
- `-fn=stats`: print only the summary (per package total function text size)

Without `-pkg`, function-level comparison builds std and cmd:

```
$ compilecmp -fn=changed  # compare generated code for std and cmd
```

To compare an out-of-tree package instead, pass its directory inside a Go
module. The comparison includes the package and its dependencies:

```
$ compilecmp -fn=changed -pkg /path/to/repo/cmd/foo
```

# Dumping SSA

If you've identified a function of interest, you might want to compare
the ssa.html output. The function name must include its package:

```
$ compilecmp -dumpssa "image/jpeg.(*decoder).processDHT"
```

This will print the path to before and after SSA html files.

# Platform

compilecmp compiles for the host platform by default. To compile for other platforms, use `-platforms`.

```
$ compilecmp -platforms=darwin/amd64,linux/arm  # compare compilation for two platforms
$ compilecmp -platforms=all  # compare compilation for all platforms
$ compilecmp -platforms=arch  # compare compilation for one platform per architecture
```

`-platforms=arch` uses Linux for non-Wasm architectures and `js/wasm` for
Wasm.

# Selecting compilation benchmarks

With `-n`, compilecmp uses `compilebench` to measure compilation time and
allocations. By default, it uses the benchmark set provided by `compilebench`.
To run just a subset:

```
$ compilecmp -n 5 -run Unicode  # runs only the unicode compiler benchmark
```

To benchmark a package outside that set, pass its directory inside a Go module
using `-pkg`:

```
$ compilecmp -n 5 -pkg /path/to/repo/cmd/foo  # test speed/allocs when compiling this package
```

# Extra compiler flags for benchmarks

With `-n`, compilecmp can pass extra compiler flags through `compilebench`. To
see how much using `-race` slows down the compiler:

```
$ compilecmp -n 5 -afterflags=-race
```

`-beforeflags` passes flags to only the "before" commit. `-flags` adds flags to
both `-beforeflags` and `-afterflags`. These flags do not affect binary,
object-file, function, or SSA comparisons.
