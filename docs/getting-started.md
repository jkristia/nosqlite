# Getting started

From a fresh clone to a passing `make all`. Run each step; read further only if
one fails.

```sh
go version        # need 1.24+   — anything older: see "the Go trap" below
gcc --version     # any C compiler; cgo needs one to build the shared library
python3 --version # 3.9+     (only if you use the Python binding)
node --version    # 22.18+   (only if you use the TypeScript binding — 22.0 is NOT enough)

make build        # produces python/nosqlite/libnosqlite.so
make all          # fmt, vet, test, build, cli, all three conformance suites
```

**A passing `make all` is the definition of "set up correctly".** The first run
also creates `.venv/` and populates `typescript/node_modules`.

`make build` is what Python and TypeScript need before they work at all — the
`.so` is not in git. Go doesn't need it.

---

## What you actually need

nosqlite is a Go engine compiled into a C shared library, plus two bindings that
load it over FFI. Each part of that sentence is one prerequisite:

| Tool | Minimum | Needed for |
|---|---|---|
| **Go** | **1.24** | everything — engine, tests, CLI, shared library |
| **A C toolchain** (`gcc`/`clang`) | any recent | `-buildmode=c-shared` runs cgo, which shells out to a real C compiler |
| **make** | any | driving all of the above |
| **Python** | 3.9+ | the Python binding and its conformance suite |
| **Node.js** | **22.18** | the TypeScript binding — Node strips `.ts` types at load, which lands in 22.18 |

Three non-obvious ones:

- **Nothing is installed into your system Python.** The binding is pure `ctypes`,
  imported straight out of `python/`. Its conformance suite needs pytest, which
  goes into `.venv/` at the repo root and is called by path, never activated.
- **The C compiler is not optional.** `make test` is pure Go, but `make build`
  turns cgo on. Without it: `cgo: C compiler "gcc" not found`.
- **Node must be ≥ 22.18, not just ≥ 22.** On an older 22.x you get a syntax
  error on the first type annotation.

Go itself has no third-party dependencies — `go.mod` has no `require` block.

---

## The Go trap on Linux/WSL

**Do not install Go from `apt`.** Ubuntu 24.04 ships 1.22, and a distro Go build
has toolchain downloads disabled, so it cannot fetch the 1.24 that `go.mod` asks
for:

```
go: download go1.24 for linux/amd64: toolchain not available
```

`GOTOOLCHAIN=local` only changes the error. The fix is a newer Go, from the
official tarball at <https://go.dev/dl/>:

```sh
curl -fLO https://go.dev/dl/go1.26.6.linux-amd64.tar.gz
sudo rm -rf /usr/local/go                      # remove any previous tarball install
sudo tar -C /usr/local -xzf go1.26.6.linux-amd64.tar.gz
echo 'export PATH="/usr/local/go/bin:$PATH"' >> ~/.bashrc
```

Without `sudo`, the same into `~/.local` — only `GOROOT/bin` being on `PATH`
matters. Then open a new shell and confirm the new Go wins:

```sh
which go        # /usr/local/go/bin/go — NOT /usr/bin/go
go version      # go1.26.6 linux/amd64
```

Still `/usr/bin/go`? Either your `PATH` line went into a file the shell doesn't
read (a login shell reads `~/.profile`, not `~/.bashrc`), or it was prepended
before `/usr/bin` was. `sudo apt remove golang-go golang-1.22-go` settles it.

The rest comes from apt, except Node, which is also too old — use
[nvm](https://github.com/nvm-sh/nvm) or NodeSource:

```sh
sudo apt install build-essential python3 python3-venv
nvm install 22
```

**macOS:**

```sh
brew install go make node python
xcode-select --install    # the C toolchain (clang)
```

**Windows:** build under **WSL**. Native Windows works in principle — the
Makefile emits `nosqlite.dll` — but cgo there needs a MinGW-w64 gcc on `PATH`,
which this project does not test. On WSL keep the clone inside the Linux
filesystem (`~/github/…`, not `/mnt/c/…`); building across the drive boundary is
noticeably slow.

---

## Verify one language at a time

The same ground as `make all` in smaller steps, so a failure points at one
language. (`make` with no target prints every target.)

```sh
make test            # Go unit tests            — needs Go only
make example         # runs examples/basic      — needs Go only
make cli             # builds ./bin/nsq         — needs Go only

make example-py      # Python binding           — needs Go + gcc + python3
make conformance-py  # Python conformance       — creates .venv on first run

make example-ts      # TypeScript binding       — needs Go + gcc + node
make ts-check        # tsc over the binding
make conformance-ts  # TypeScript conformance
```

Dependency-installing targets guard themselves and run once. `.venv/` and
`node_modules` are gitignored; `make clean` removes them, along with the
`db/example.nsq` and `.trace` files the examples leave behind for you to inspect.

---

## Troubleshooting

| What you see | What it means |
|---|---|
| `download go1.24 …: toolchain not available` | Go is older than 1.24 and can't self-upgrade. See the Go trap above. |
| `go.mod requires go >= 1.24 (running go 1.22)` | Same cause, shown when toolchain downloads are off. |
| `cgo: C compiler "gcc" not found` | No C toolchain. `sudo apt install build-essential`. |
| `ModuleNotFoundError: No module named 'nosqlite'` | Run via `make example-py`, or set `PYTHONPATH=python` — the package is never installed. |
| `OSError: … libnosqlite.so: cannot open shared object file` | `make build` hasn't run, or ran for a different platform. |
| `Cannot find module 'koffi'` | `typescript/node_modules` is missing — `make ts-deps`. |
| `SyntaxError` on a type annotation in a `.ts` file | Node older than 22.18. |
| `ensurepip is not available` | `python3` is there but venv is packaged separately. `sudo apt install python3-venv`. |
| `error: externally-managed-environment` | You ran `pip install` against the system Python. Don't — `make py-deps` uses `.venv/`. |
| `Makefile:NN: *** missing separator` | An edit put spaces where a recipe line needs a TAB. |

---

## Editor setup (optional)

Three files exist so an editor sees what the runtime sees; none affect the build.
Without them the examples are full of red underlines that say nothing about the
code.

- [`pyrightconfig.json`](../pyrightconfig.json) puts `python/` on Pylance's
  import path.
- [`examples/tsconfig.json`](../examples/tsconfig.json) gives the TypeScript
  language server a config to find above `examples/basic/basic.ts`, and
  [`examples/basic/package.json`](../examples/basic/package.json) is two lines
  marking that directory ES-module.
- [`.vscode/settings.json`](../.vscode/settings.json) points VS Code at
  `.venv/bin/python`. Other editors discover a root `.venv` on their own.

`make ts-deps` must have run once, or the editor cannot resolve `koffi`.

---

## Where to go next

- [`api.md`](api.md) — the full API in all three languages
- [`nosql-primer.md`](nosql-primer.md) — if *collection* and *document* are new terms
- [`go-primer/`](go-primer/) — if Go is new
- [`design.md`](design.md) — what nosqlite is and why
- [`testing.md`](testing.md) — where a new test belongs
