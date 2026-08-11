# 11. Idioms and gotchas

The things that compile fine and behave surprisingly, plus the style rules Go reviewers
apply automatically.

---

## 11.1 The top gotchas

### 1. `append` aliases the backing array

```go
a := []int{1,2,3,4,5}
b := append(a[:2], 99)     // a becomes [1 2 99 4 5]
```
Whether the original is mutated depends on **capacity**. Copy defensively:
`slices.Clone(s)` or `append([]int(nil), s...)`. See ch. 5.

### 2. Writing to a nil map panics

```go
var m map[string]int
m["k"] = 1              // panic
```
Reading is fine. `make` (or a literal) before you write. Same for a nil map field in a
zero-value struct.

### 3. Loop variable capture (fixed in Go 1.22)

```go
for _, v := range xs {
    go func() { use(v) }()
}
```
- `go 1.22`+ in `go.mod`: `v` is a fresh variable each iteration. Correct.
- Earlier: all goroutines share one `v` and usually see the last element.

Check the `go` directive in `go.mod` — it decides which semantics you get.

### 4. Range copies the element

```go
for _, u := range users { u.Age++ }        // mutates a COPY, no effect
for i := range users    { users[i].Age++ } // correct
```

### 5. A nil pointer inside an interface is not nil

```go
var p *MyErr = nil
var err error = p
err != nil       // true!
```
See ch. 4.6. Return a literal `nil`, never a typed nil variable.

### 6. `defer` in a loop runs at function exit

Deferring `f.Close()` inside a loop keeps every file open until the function returns.
Extract the body into a function.

### 7. `defer` evaluates arguments immediately

```go
start := time.Now()
defer fmt.Println(time.Since(start))        // prints ~0
defer func() { fmt.Println(time.Since(start)) }()   // correct
```

### 8. `os.Exit` and `log.Fatal` skip all defers

Files stay unflushed, locks unreleased. Use them only at the very top of `main`.

### 9. Map iteration order is randomized

Deliberately, on every run. Sort keys if output must be stable.

### 10. `string(65)` is `"A"`, not `"65"`

Use `strconv.Itoa`. `go vet` flags the common cases.

### 11. Comparing `time.Time` with `==`

Compares the monotonic clock reading and location too. Use `t1.Equal(t2)`.

### 12. Copying a struct that contains a mutex

Breaks the lock silently. Always use pointer receivers on such types; `go vet` catches
most of it.

### 13. Unbuffered channel send blocks forever with no receiver

Classic deadlock in `main`. Buffer it, or start the receiver first.

### 14. Shadowed `err` in an inner scope

```go
if err := f(); err != nil { }   // this err never escapes the if
```
Fine when intended, a silent bug when not. `go vet -vettool=shadow` or `staticcheck` help.

### 15. Integer division truncates

`3/2 == 1`. Convert first: `float64(3)/2`.

### 16. Not closing an HTTP response body leaks connections

`defer resp.Body.Close()` after every successful `Do`.

### 17. `%v` on a struct pointer prints an address

Use `%+v` on the value, or implement `String()`.

### 18. Slices of structs vs slices of pointers

`[]T` copies on every read; `[]*T` gives shared mutable elements but scatters memory and
adds GC pressure. Default to `[]T` for small structs, `[]*T` when you mutate elements
in place.

---

## 11.2 Style rules Go reviewers apply

**Keep the happy path at the left margin.** Handle the error and return early; don't
wrap the success case in an `else`.

```go
// ✅
if err != nil { return err }
if !ok       { return ErrX }
doWork()
```

**Name things for the caller, not the implementation.**
- Package name is part of the identifier: `http.Client`, not `http.HTTPClient`.
- Short receiver and loop names (`s`, `i`, `k`) — long descriptive names for
  package-level and exported identifiers.
- Variable name length ∝ scope size. `i` in a 3-line loop, `userCount` at package level.

**Return concrete types, accept interfaces.** (ch. 4)

**Make the zero value useful.** `var b bytes.Buffer` should work without a constructor.

**Don't export what you don't have to.** Unexported first; export when a caller needs it.

**No getters/setters by default.** Exported fields are fine. `Name()`, not `GetName()`,
when you do need a method.

**Return errors, don't log them, in libraries.** Logging is the application's decision.

**Every exported identifier gets a doc comment starting with its name.**

**Don't panic across a package boundary.** Convert to an error.

**Preallocate when you know the size:** `make([]T, 0, n)`.

**Use `context.Context` as the first parameter** of anything that blocks, and never
store it in a struct.

**Prefer standard library.** Small dependency trees are a Go cultural value, not just
a preference.

---

## 11.3 Small idioms worth memorizing

```go
v, ok := m[k]                              // comma-ok
if v, ok := m[k]; ok { }                   // ...scoped
x, y = y, x                                // swap
s = s[:0]                                  // truncate, keep capacity
_ = unusedButNeeded                        // explicit discard
var _ io.Reader = (*T)(nil)                // compile-time interface check
defer close(done)                          // broadcast completion
struct{}{}                                 // zero-byte value: sets, signals
make([]T, 0, n)                            // preallocate
b, err := json.Marshal(v); if err != nil { }
for i := range n { }                       // Go 1.22+ count loop
errors.Is(err, ErrX) / errors.As(err, &t)  // never == on wrapped errors
fmt.Errorf("ctx: %w", err)                 // add context, keep the cause
t.Helper()                                 // in test helpers
```

---

## 11.4 Reading a Go file quickly

Top-to-bottom, a well-organized file goes:

1. `// Package x ...` doc comment (one file per package has it)
2. `package x`
3. `import ( ... )` — stdlib group, blank line, external group
4. Constants (`const`, `iota` blocks)
5. Package-level `var`s — including sentinel errors `ErrX`
6. Types, most important first
7. `New…` constructor
8. Exported methods
9. Unexported helpers

To find something: `grep "func (.*Foo)"` gives every method of `Foo`, wherever it lives
in the package.

---

## 11.5 Where to look things up

```bash
go doc strings                  # package summary
go doc strings.Builder          # a type + its methods
go doc -src strings.Contains    # the actual source
go doc -all encoding/json | less
```

- `pkg.go.dev` — rendered docs for stdlib and every public module.
- `go.dev/ref/spec` — the language spec. Short and surprisingly readable.
- `go.dev/doc/effective_go` — the original style document.
- `github.com/golang/go/wiki/CodeReviewComments` — the checklist Go reviewers use.
- The stdlib source (`$(go env GOROOT)/src`) is idiomatic, readable Go. Read it.
