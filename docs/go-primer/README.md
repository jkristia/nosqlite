# Go primer — a reference cheat sheet

Written for a developer fluent in **TypeScript, Python and C#** who is new to Go.
Every section is "how to do this — and why", with the C#/TS/Python equivalent named
where it helps. Short bullets, many small examples, no essays.

Nothing here is specific to this repo. It is a general Go reference.

## Chapters

| # | Doc | Covers |
| --- | --- | --- |
| 0 | [00-quick-reference.md](00-quick-reference.md) | One page. Syntax, keywords, commands. Start here, come back here. |
| 1 | [01-layout-and-modules.md](01-layout-and-modules.md) | Folders, modules, packages, imports, visibility, `internal/`, `cmd/` |
| 2 | [02-syntax.md](02-syntax.md) | Variables, types, control flow, functions, `defer`, zero values |
| 3 | [03-structs-and-methods.md](03-structs-and-methods.md) | Structs, pointers, receivers, embedding, constructors |
| 4 | [04-interfaces.md](04-interfaces.md) | Implicit interfaces, `any`, type switches, nil traps |
| 5 | [05-slices-maps-strings.md](05-slices-maps-strings.md) | Arrays vs slices, `append` aliasing, maps, runes vs bytes |
| 6 | [06-errors.md](06-errors.md) | `error` values, wrapping, `errors.Is/As`, `panic`/`recover` |
| 7 | [07-concurrency.md](07-concurrency.md) | Goroutines, channels, `select`, mutexes, `context` |
| 8 | [08-stdlib.md](08-stdlib.md) | `fmt`, `encoding/json`, `os`, `io`, `strings`, `sort`, `time`, `flag` |
| 9 | [09-testing-and-tooling.md](09-testing-and-tooling.md) | `go test`, table tests, benchmarks, race detector, `go vet` |
| 10 | [10-generics.md](10-generics.md) | Type parameters, constraints, when not to bother |
| 11 | [11-idioms-and-gotchas.md](11-idioms-and-gotchas.md) | Naming, the traps that bite newcomers, review checklist |
| 12 | [12-cgo.md](12-cgo.md) | Calling C from Go, building shared libraries, the pointer rules |

## The five things that surprise OOP developers most

1. **No classes.** A `struct` holds data; methods are declared *outside* it, attached by a
   receiver. See [ch. 3](03-structs-and-methods.md).
2. **No `public`/`private` keyword.** A capital first letter exports the name; lowercase
   keeps it inside the **package** (a folder), not inside the type. See [ch. 1](01-layout-and-modules.md).
3. **No inheritance.** Composition + interfaces only. Interfaces are satisfied
   *implicitly* — no `implements`. See [ch. 4](04-interfaces.md).
4. **No exceptions.** Errors are ordinary return values you must handle. See [ch. 6](06-errors.md).
5. **No `null` for most things.** Every type has a usable *zero value*, and a
   zero-value struct is often ready to use. See [ch. 2](02-syntax.md).
