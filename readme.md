compilecmp is a bit of a Swiss Army knife for Go compiler developers. It compares the compiler at different git commits. It can compare compilation time and memory usage, the sizes of the generated binaries, the sizes of the generated object files, and the generated code.

Two important caveats:
* compilecmp measures the toolchain (GOROOT) it was compiled with
* compilecmp (unlike toolstash) uses the toolchain on itself, so if you (say) add a bunch of code to text/template, compilecmp will report that text/template got slower to compile; since the compiler itself is one of the subject packages, it can be ambiguous why a performance change for those entries occurred

# Specifying commits

compilecmp accepts up to two git refs as arguments.

* If no refs are provided, it assumes you are measuring from master to HEAD.

```
$ compilecmp  # compares master to head
```

* If one ref is provided, compilecmp treats it as the before commit.

```
$ compilecmp head~1  # compares current commit to its parent
```

* If two refs are provided, they are treated as the before and after commits.

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

# Number of runs

Some compiler outputs are always the same, like the generated code, object files, and binaries.

Others require multiple runs to measure accurately, such as allocations and CPU time. The `-n` flag lets you run multiple iterations.

```
$ compilecmp -n 5  # run five iterations
```

`-n 5` is a good number for measuring allocations.

`-n 50` is a good number for measuring CPU time. `-n 100` is better, particularly if you are trying to detect small changes. This takes a long time! compilecmp will print an ETA. Be sure to quit all other running apps, including backups (like Time Machine). I also suggest using `-cpu` in this case, to suppress memory profiling, which yields more consistent results.

When `n > 0`, compilecmp also does a uncounted warmup run at the beginning.

# Compare files sizes

By default, compilecmp prints the sizes of executables such as cmd/addr2line.

`-obj` adds object files sizes. Beware that object sizes aren’t always correlated to compilation quality! There’s lots of other stuff in there: dwarf, pclntab, export information, etc.

# Comparing generated code

compilecmp can also compare the generated code, function by function. (This part is still in flux a bit.)

* `-fn=all`: print all functions whose contents have changed
* `-fn=changed`: print all functions whose text size has changed
* `-fn=smaller`: print all functions whose text size has gotten smaller
* `-fn=bigger`: print all functions whose text size has gotten bigger
* `-fn=stats`: print only the summary (per package total function text size)

# Platform

compilecmp compiles for the host platform by default. To compile for other platforms, use `-platforms`.

```
$ compilecmp -platforms=darwin/amd64,linux/arm  # compare compilation for two platforms
$ compilecmp -platforms=all  # compare compilation for all platforms
$ compilecmp -platforms=arch  # compare compilation for one platform per architecture
```

# Limiting the set of benchmarks

By default, compilecmp uses the benchmarks from `compilebench`. To run just a subset of those:

```
$ compilecmp -run Unicode  # runs only the unicode compiler benchmark
```

You can also run benchmarks for any package, not necessarily only those in `compilebench`, by using `-pkg`.

```
$ compilebench -pkg github.com/pkg/diff  # test speed/allocs when compiling this package
```

# Extra compiler flags

compilecmp can pass extra compiler flags. To see how much using `-race` slows down the compiler:

```
$ compilecmp -afterflags=-race
```

`-beforeflags` passes flags to only the "before" commit. `flags` adds flags to both `-beforeflags` and `-afterflags`.
