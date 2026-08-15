# nosqlite — getting started

From a fresh clone to a passing `make all`, on a machine that has nothing installed
yet. For what nosqlite *is*, see the [README](../README.md).

Run each check; read further only if one fails.

**1. Go.**

```sh
go version        # require go1.24 or newer
```

Anything older, or `toolchain not available`, or `go.mod requires go >= 1.24` →
**[§2](#2-installing-the-toolchain--and-the-go-trap-on-linuxwsl)**. On Ubuntu/WSL the `apt`
package is 1.22 and cannot upgrade itself, so this step fails on a stock machine
even though `go` is installed.

**2. A C compiler** — cgo needs one to build the shared library.

```sh
gcc --version     # any version; `sudo apt install build-essential` if missing
```

**3. Per-binding runtimes** — skip whichever language you won't use.

```sh
python3 --version # require 3.9+   (Python binding)
node --version    # require v22.18.0+ (TypeScript binding — 22.0 is NOT enough)
```

Missing or too old → **[§2](#2-installing-the-toolchain--and-the-go-trap-on-linuxwsl)**.

**4. Build.**

```sh
make build
```

This produces `python/nosqlite/libnosqlite.so`, which is not in git. Python and
TypeScript do not work at all until it exists; Go doesn't need it.

**5. Confirm the whole thing.**

```sh
make all          # fmt, vet, test, build, cli, all three conformance suites
```

A passing `make all` is the definition of "set up correctly". The first run also
creates `.venv/` and populates `typescript/node_modules`.

One language at a time → **[§3](#3-verify-one-language-at-a-time)**. An error you
don't recognise → **[§4](#4-troubleshooting)**.

---

## 1. What you actually need

nosqlite is a Go engine compiled into a C shared library, plus two bindings that
load it over FFI. Each part of that sentence is one prerequisite:

| Tool | Minimum | Needed for | Needed if you only… |
|---|---|---|---|
| **Go** | **1.24** | everything — the engine, the tests, the CLI, the shared library | always |
| **A C toolchain** (`gcc` or `clang`) | any recent | `-buildmode=c-shared` runs cgo, which shells out to a real C compiler | building the library, i.e. anything but `make test` |
| **make** | any | driving all of the above | always (or run the `go`/`npm` commands by hand) |
| **Python** | 3.9+ | the Python binding and its conformance suite | using Python |
| **Node.js** | **22.18** | the TypeScript binding — Node runs `.ts` files directly by stripping types, which lands in 22.18 | using TypeScript |

Three non-obvious ones:

- **Nothing is installed into your system Python.** The binding is pure `ctypes`
  imported straight out of `python/`, with no dependencies. Its conformance suite
  needs pytest, which goes into `.venv/` at the repo root — created by
  `make py-deps`, called by path as `.venv/bin/pytest`, never activated. On
  Debian/Ubuntu `python3 -m venv` is a separate apt package, so this is the one
  step that can fail; see §2.
- **The C compiler is not optional.** `make test` is pure Go, but `make build`
  compiles `capi/` with `-buildmode=c-shared`, which turns cgo on. Without `gcc`
  you get `cgo: C compiler "gcc" not found`.
- **Node must be ≥ 22.18, not just ≥ 22.** The examples and the binding are `.ts`
  files executed directly, and type stripping became on-by-default in 22.18. On an
  older 22.x you get a syntax error on the first type annotation.

Go itself has no third-party dependencies — the design mandates the standard
library, so `go.mod` has no `require` block.

---

## 2. Installing the toolchain — and the Go trap on Linux/WSL

**Do not install Go from `apt`.** Ubuntu 24.04 ships Go 1.22, and this project
needs 1.24. Normally that would be harmless: a modern `go` command notices the
`go 1.24` line in `go.mod` and downloads the matching toolchain for you. Distro
Go builds have that download disabled, so instead you get:

```
go: downloading go1.24 (linux/amd64)
go: download go1.24 for linux/amd64: toolchain not available
make: *** [Makefile:48: build] Error 1
```

`GOTOOLCHAIN=local` does not help — it only changes the error. The fix is a newer
Go.

### Linux / WSL — official tarball

Pick the current version from <https://go.dev/dl/>. With `sudo`:

```sh
curl -fLO https://go.dev/dl/go1.26.6.linux-amd64.tar.gz
sudo rm -rf /usr/local/go                      # remove any previous tarball install
sudo tar -C /usr/local -xzf go1.26.6.linux-amd64.tar.gz
echo 'export PATH="/usr/local/go/bin:$PATH"' >> ~/.bashrc
```

Without `sudo`, the same in your home directory — only `GOROOT/bin` being on
`PATH` matters:

```sh
curl -fLO https://go.dev/dl/go1.26.6.linux-amd64.tar.gz
rm -rf ~/.local/go
tar -C ~/.local -xzf go1.26.6.linux-amd64.tar.gz
echo 'export PATH="$HOME/.local/go/bin:$PATH"' >> ~/.bashrc
```

Open a new shell (or `source ~/.bashrc`) and confirm the new Go wins:

```sh
which go        # /usr/local/go/bin/go — NOT /usr/bin/go
go version      # go1.26.6 linux/amd64
```

If `which go` still says `/usr/bin/go`, the apt package is shadowing the new one:
either your `PATH` line went into a file your shell doesn't read (a login shell
reads `~/.profile`, not `~/.bashrc`), or it was prepended before `/usr/bin` was.
`sudo apt remove golang-go golang-1.22-go` settles it permanently.

The rest comes from apt:

```sh
sudo apt update
sudo apt install build-essential python3 python3-venv
```

Ubuntu's Node is also too old. Use [nvm](https://github.com/nvm-sh/nvm) or
[NodeSource](https://github.com/nodesource/distributions):

```sh
curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash
# reopen the shell, then:
nvm install 22
```

### macOS

```sh
brew install go make node python
xcode-select --install    # the C toolchain (clang)
```

Homebrew's Go is current, so the version problem above does not apply.

### Windows

Build under **WSL** and follow the Linux instructions. Native Windows works in
principle — the Makefile emits `nosqlite.dll` — but cgo there needs a MinGW-w64
gcc on `PATH`, which this project does not test.

On WSL, keep the clone inside the Linux filesystem (`~/github/...`, not
`/mnt/c/...`); building across the drive boundary is noticeably slow.

---

## 3. Verify, one language at a time

The same ground as `make all`, in smaller steps, so a failure points at one
language. Each line needs only the tools listed for it. (`make` with no target
prints every target.)

```sh
make test            # Go unit tests            — needs Go only
make example         # runs examples/basic      — needs Go only
make cli             # builds ./bin/nsq         — needs Go only

make example-py      # Python binding           — needs Go + gcc + python3
make conformance-py  # Python conformance suite — creates .venv on first run

make example-ts      # TypeScript binding       — needs Go + gcc + node
make ts-check        # tsc over the binding
make conformance-ts  # TypeScript conformance suite
```

The dependency-installing targets guard themselves and only run once:
`conformance-py` creates `.venv/` and pip-installs `requirements-dev.txt` into
it; the `-ts` targets run `npm install` when `node_modules` is missing. Both
directories are gitignored, and `make clean` removes them.

The examples write `db/example.nsq` and a `.trace` file beside it, and leave them
for you to inspect. `make clean` removes them.

---

## 4. Troubleshooting

| What you see | What it means |
|---|---|
| `download go1.24 …: toolchain not available` | Go is older than 1.24 and can't self-upgrade. §2. |
| `go.mod requires go >= 1.24 (running go 1.22)` | Same cause, shown when toolchain downloads are off. §2. |
| `cgo: C compiler "gcc" not found` | No C toolchain. `sudo apt install build-essential`. |
| `ModuleNotFoundError: No module named 'nosqlite'` | Run via `make example-py`, or set `PYTHONPATH=python` yourself — the package is imported straight out of the checkout, never installed. |
| `OSError: … libnosqlite.so: cannot open shared object file` | `make build` hasn't run, or was run for a different platform. |
| `Cannot find module 'koffi'` | `typescript/node_modules` is missing — `make ts-deps`. |
| `SyntaxError` on a type annotation in a `.ts` file | Node older than 22.18. §1. |
| `ensurepip is not available` / `apt install python3.12-venv` | `python3` is there but the venv module isn't — it's packaged separately. `sudo apt install python3-venv`, then `make conformance-py` again. |
| `error: externally-managed-environment` | You ran `pip install` against the system Python. Don't — `make py-deps` puts pytest in `.venv/`. |
| `Makefile:NN: *** missing separator` | An edit put spaces where a recipe line needs a TAB. |

---

## 5. Editor setup (optional)

These files exist so an editor sees what the runtime sees; none affect the build.
[`pyrightconfig.json`](../pyrightconfig.json) puts `python/` on Pylance's import
path, [`examples/tsconfig.json`](../examples/tsconfig.json) gives the TypeScript
language server something to find above `examples/basic/basic.ts`, and
[`examples/basic/package.json`](../examples/basic/package.json) marks that
directory ES-module. `make ts-deps` must also have run once, or the editor cannot
resolve `koffi`.

[`.vscode/settings.json`](../.vscode/settings.json) points VS Code at
`.venv/bin/python`. Other editors discover a root `.venv` on their own. The build
never relies on this — `make` calls `.venv/bin/pytest` by path.

---

## Where to go next

- [`docs/nosql-primer.md`](nosql-primer.md) — if *collection* and *document* are new terms
- [`docs/go-primer/`](go-primer/) — if Go is new
- [`docs/design.md`](design.md) — what nosqlite is and why
- [`docs/testing.md`](testing.md) — where a new test belongs
