# 1. Folder structure, modules, packages

The part that feels most alien coming from C#/TS/Python. Go has **no project file**
listing sources, **no namespace declaration independent of the folder**, and
**no per-class access modifiers**.

---

## 1.1 The three units

| Unit | Is | Analogous to |
| --- | --- | --- |
| **Module** | A directory tree with a `go.mod` at its root. The dependency + versioning unit. | `package.json` / `.csproj` / `pyproject.toml` |
| **Package** | **One directory** of `.go` files sharing a `package X` line. The compilation + visibility unit. | C# namespace-ish, Python package-ish |
| **File** | Just a file. Has no meaning of its own. | — |

Key consequence: **a package is exactly one folder — not the folder plus its
subfolders.** `foo/` and `foo/bar/` are two unrelated packages. Nesting only affects
the import path string, never visibility.

## 1.2 go.mod

```go
module github.com/you/proj      // module path = import path prefix

go 1.22                          // minimum toolchain / language version

require (
    github.com/some/dep v1.4.0
)
```

- The module path does **not** have to exist on GitHub for local builds.
- `go 1.22` also selects **language semantics** (e.g. the loop-variable fix — see ch. 11).
- `go.sum` is the lockfile of hashes. Commit both.
- No `node_modules`. Deps live in a global cache at `$GOPATH/pkg/mod`.

```bash
go mod init github.com/you/proj
go get github.com/some/dep@v1.4.0   # add / upgrade one dep
go mod tidy                          # sync go.mod+go.sum to actual imports
go mod why github.com/some/dep       # who pulled this in?
go list -m all                       # full dependency graph
```

## 1.3 A typical layout

```
proj/
├── go.mod                  module github.com/you/proj
├── go.sum
├── Makefile
├── thing.go        ─┐
├── other.go         ├─ package proj      →  import "github.com/you/proj"
├── thing_test.go   ─┘
├── cmd/
│   └── app/
│       └── main.go         package main  →  the executable
├── internal/
│   └── engine/
│       └── engine.go       package engine →  import ".../internal/engine"
├── pkg/                    (optional) public sub-libraries
└── docs/
```

Rules of thumb:

- **Library at the root** if the module *is* one library. Consumers then write
  `import "github.com/you/proj"`.
- **`cmd/<name>/main.go`** for each executable. One folder per binary.
- **`internal/`** is enforced by the compiler: anything under `internal/` can only be
  imported by code rooted at `internal/`'s parent. This is the only real "assembly-private"
  Go has.
- **`pkg/`** is a convention, not a rule. Skip it unless the root is getting crowded.
- Don't create a folder per type. Folders are packages; packages should be *concepts*.

## 1.4 Files vs types — no "one class per file"

Idiomatic Go groups methods **by concern**, not by type. A single type's methods
routinely live in several files:

```
store.go      func (d *DB) Read(...)   func (d *DB) Write(...)
catalog.go    func (d *DB) Collections(...)
trace.go      func (d *DB) trace(...)
```

All are `package db`, so they all see each other's unexported members. Coming from C#,
this is closest to `partial class` — except it is the default, not a keyword.

## 1.5 Visibility: capitalization, package-scoped

```go
package store

type Entry struct {   // exported: usable as store.Entry
    Key   string      // exported field
    value []byte      // unexported field
}

func New() *Entry { }   // exported
func decode() { }       // unexported
```

- **Capital first letter = exported** from the package. Lowercase = not.
- Applies to types, funcs, methods, fields, constants, everything.
- The boundary is the **package**, not the type. Any file in `package store` can read
  `Entry.value`. There is no `private` in the C#/TS sense.

| C# / TS | Go |
| --- | --- |
| `public` | Capitalized name |
| `internal` (assembly) | `internal/` directory |
| `private` (class) | *no equivalent* — closest is "unexported + keep the package small" |
| `protected` | *no equivalent* — no inheritance |

## 1.6 Imports

```go
import (
    "fmt"                                  // stdlib: no prefix
    "net/http"

    "github.com/you/proj/internal/engine"  // module-relative, full path
    js "encoding/json"                     // alias
    _ "github.com/lib/pq"                  // blank: run init() only, no direct use
    . "math"                               // dot import — don't
)
```

- You import a **directory path**, and refer to it by the **package name declared in
  those files** (usually the last path segment, but not guaranteed).
- No relative imports (`../foo`). Always the full module path.
- No re-export / barrel files. No `export *`.
- **Import cycles are a hard compile error.** A ↔ B is impossible; extract a third
  package or use an interface at the consumer side.

## 1.7 `main`

```go
package main            // special name: builds an executable, not a library

func main() { }         // entry point, no args, no return
```

- Exit code: `os.Exit(1)`, or return from `main` for 0.
- Args: `os.Args` (`os.Args[0]` is the program), or the `flag` package (ch. 8).

## 1.8 init()

```go
func init() { }   // runs once, after package vars are set, before main
```

- Can appear multiple times, even in one file. Runs in file-name order.
- Use sparingly — hidden startup work is hard to test. Prefer explicit `New()`.

## 1.9 Build tags

Conditional compilation, replacing `#if` in C#:

```go
//go:build linux && amd64

package thing
```

Filename suffixes do the same implicitly: `file_linux.go`, `file_windows.go`,
`file_test.go`, `file_arm64.go`.
