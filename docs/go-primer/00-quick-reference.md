# 0. Quick reference

One page. Everything else in this folder is an expansion of something here.

---

## Declarations

```go
var a int              // zero value: 0
var b = 42             // type inferred: int
c := 42                // short form — only inside a function
const Pi = 3.14159     // compile-time constant
var x, y = 1, "two"    // multiple at once

var (                  // grouped — common at package level
    debug   bool
    version = "1.0"
)
```

- `:=` declares **and** assigns; `=` only assigns. `:=` is illegal at package level.
- Unused **local** variables and unused **imports** are compile errors, not warnings.

## Zero values — there is no `undefined`

| Type | Zero |
| --- | --- |
| `int`, `float64` | `0` |
| `string` | `""` |
| `bool` | `false` |
| pointer, slice, map, chan, func, interface | `nil` |
| struct | struct with every field at its own zero |

## Basic types

```go
bool
string
int  int8 int16 int32 int64        // int is 64-bit on modern platforms
uint uint8 uint16 uint32 uint64
byte    // = uint8
rune    // = int32, one Unicode code point
float32 float64
complex64 complex128
error   // an interface, see ch. 6
any     // = interface{}, "anything"
```

**No implicit conversion at all** — not even `int` → `int64`:

```go
var i int = 3
var f float64 = float64(i)   // required
```

## Control flow

```go
if err != nil { ... } else if x > 0 { ... } else { ... }

if v, err := doThing(); err != nil {   // scoped to the if/else
    return err
}

for i := 0; i < 10; i++ { }        // classic
for x < 10 { }                     // while
for { break }                      // infinite
for i, v := range slice { }        // index, value
for k, v := range m { }            // key, value (random order!)
for i := range 10 { }              // 0..9   (Go 1.22+)

switch {                           // no condition = switch true
case x > 10: ...
default: ...
}

switch day {
case "sat", "sun": ...             // no fallthrough by default
}
```

- No parentheses around conditions. Braces are **mandatory**.
- `switch` cases do not fall through; use `fallthrough` if you really want it.

## Functions

```go
func add(a, b int) int { return a + b }

func divmod(a, b int) (int, int) { return a / b, a % b }   // multiple returns

func read() (n int, err error) {   // named results
    n = 5
    return                         // "naked return" — returns n, err
}

func sum(nums ...int) int { }      // variadic
sum(1, 2, 3)
sum(slice...)                      // spread

f := func(x int) int { return x * 2 }   // closure / lambda
```

## defer

```go
f, err := os.Open(name)
if err != nil { return err }
defer f.Close()          // runs when the function returns, LIFO order
```

Go's `try/finally` and C#'s `using`, in one line at the point of acquisition.

## Structs & methods

```go
type Point struct {
    X, Y int
    name string          // lowercase = package-private
}

p := Point{X: 1, Y: 2}   // literal
q := &Point{X: 1}        // pointer to a new one
var z Point              // zero value, usable

func (p Point) Sum() int   { return p.X + p.Y }  // value receiver: gets a copy
func (p *Point) Move(d int) { p.X += d }         // pointer receiver: can mutate
```

- `p.Move(1)` works even when `p` is a value — Go auto-takes the address.
- Rule of thumb: **use pointer receivers** unless the type is tiny and immutable.

## Interfaces

```go
type Reader interface {
    Read(p []byte) (n int, err error)
}
```

- No `implements`. Any type with that method set satisfies it automatically.
- Keep interfaces **small** and declare them where they are *consumed*, not where implemented.

```go
switch v := val.(type) {          // type switch
case string: fmt.Println(v)
case int:    fmt.Println(v * 2)
}

s, ok := val.(string)             // safe type assertion
```

## Slices & maps

```go
var s []int                       // nil, len 0 — append works fine
s = append(s, 1, 2, 3)
s2 := make([]int, 0, 10)          // len 0, cap 10
s[1:3]                            // half-open slice, shares memory!
copy(dst, src)
len(s); cap(s)

m := map[string]int{"a": 1}
m := make(map[string]int)
v, ok := m["a"]                   // ok = false if missing
delete(m, "a")
```

- Reading a missing key returns the **zero value**, never an error.
- Writing to a `nil` map **panics**. Always `make` a map before writing.

## Errors

```go
if err != nil {
    return fmt.Errorf("loading %s: %w", name, err)   // %w wraps
}

errors.Is(err, os.ErrNotExist)     // match a sentinel
var pe *fs.PathError
errors.As(err, &pe)                // match a type
```

## Goroutines & channels

```go
go doWork()                        // fire off a goroutine

ch := make(chan int)               // unbuffered
ch := make(chan int, 100)          // buffered
ch <- 1                            // send
v := <-ch                          // receive
v, ok := <-ch                      // ok = false if closed & drained
close(ch)
for v := range ch { }              // until closed

select {
case v := <-ch:  ...
case ch2 <- 1:   ...
default:         ...               // non-blocking
}

var mu sync.Mutex
mu.Lock(); defer mu.Unlock()
```

## Package & imports

```go
package mypkg

import (
    "fmt"                          // stdlib
    "os"

    "github.com/you/proj/internal/engine"   // your module
)
```

- File's folder = package. One package per folder.
- `Exported` = capital. `unexported` = lowercase, visible to the whole package.
- Unused import = compile error. Let `goimports`/`gopls` manage the block.

## Commands you actually use

| Command | Does |
| --- | --- |
| `go mod init github.com/you/proj` | Start a module (like `npm init`) |
| `go mod tidy` | Add missing / drop unused deps |
| `go build ./...` | Compile everything |
| `go run ./cmd/app` | Compile + run |
| `go test ./...` | Run all tests |
| `go test -run TestFoo -v ./pkg` | One test, verbose |
| `go test -race ./...` | Run with the data-race detector |
| `go test -bench=. -benchmem` | Benchmarks |
| `go test -cover ./...` | Coverage |
| `go vet ./...` | Static checks beyond the compiler |
| `gofmt -l -w .` | Format (non-negotiable in Go) |
| `go doc strings.Builder` | Docs in the terminal |
| `go install ./cmd/app` | Build a binary into `$GOBIN` |

## Naming conventions

| Thing | Go style | Not |
| --- | --- | --- |
| Package | `engine`, `json` — short, lowercase, no underscores | `myEngine`, `engine_utils` |
| Exported func | `ReadFile` | `readFile`, `read_file` |
| Unexported | `readFile` | `_readFile` |
| Interface | `Reader`, `Stringer` — `-er` suffix | `IReader` |
| Getter | `Name()` | `GetName()` |
| Setter | `SetName()` | `setName()` |
| Constructor | `New`, `NewClient` | `CreateClient` |
| Acronyms | `URL`, `ID`, `HTTP` — all one case | `Url`, `Id`, `Http` |
| Errors | `ErrNotFound`, type `FooError` | |
| Test | `TestThing`, file `thing_test.go` | |
