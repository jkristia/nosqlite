# 2. Syntax essentials

---

## 2.1 Variables

```go
var a int            // declared, zero value 0
var b int = 5
var c = 5            // type inferred
d := 5               // short form, function scope only

a, b = b, a          // swap, no temp
x, _ := twoResults() // _ discards a value
```

- **Why so many forms?** `var` at package level, `:=` inside functions. That's the whole rule.
- Unused local = **compile error**. Assign to `_` if you truly must keep it.
- Shadowing is legal and a common bug source:

```go
err := f()
if true {
    err := g()   // NEW err — the outer one is untouched
    _ = err
}
```

## 2.2 Constants and iota

```go
const MaxSize = 1024                // untyped constant — adapts to context
const Greeting string = "hi"        // typed

type Level int
const (
    Debug Level = iota   // 0
    Info                 // 1
    Warn                 // 2
    Error                // 3
)

const (
    KB = 1 << (10 * (iota + 1))   // 1024
    MB                            // 1048576
)
```

- `iota` is Go's enum generator. There is no real `enum` type — you get a named
  integer type plus constants, which is weaker than C#'s `enum` (no exhaustiveness check).
- Give it a `String()` method for printing (ch. 4), or generate one with `stringer`.

## 2.3 Types you declare

```go
type UserID int64            // distinct type — NOT interchangeable with int64
type Celsius float64
type Handler func(int) error // function type
type Pairs map[string]int    // named map
```

```go
var id UserID = 5
var n int64 = id             // compile error
var n int64 = int64(id)      // ok
```

This is TypeScript's *branded type*, but built in and enforced everywhere.

## 2.4 Conversion vs assertion vs parsing

```go
float64(i)                   // conversion — between compatible types
val.(string)                 // assertion — pull a concrete type out of an interface
strconv.Atoi("42")           // parsing  — string → int, returns (int, error)
strconv.Itoa(42)             // int → string
fmt.Sprintf("%d", 42)        // formatting
```

⚠️ `string(65)` is `"A"` (rune conversion), not `"65"`. Use `strconv`.

## 2.5 Control flow

```go
if x > 0 {
} else if x < 0 {
} else {
}

if v, err := parse(s); err == nil {   // init statement; v and err scoped to the if
    use(v)
}
```

- No ternary operator. Write the `if`.
- No truthiness: the condition must be a `bool`. `if x` where `x` is an `int` is an error.

```go
for i := 0; i < n; i++ {}
for cond {}                  // while
for {}                       // forever
for i := range n {}          // 0..n-1, Go 1.22+
for i, v := range xs {}      // slice: index, copy of value
for k, v := range m {}       // map: RANDOM order every run
for i, r := range s {}       // string: byte index, rune
for v := range ch {}         // channel: until closed

outer:
for _, a := range as {
    for _, b := range bs {
        if a == b { break outer }   // labeled break/continue
    }
}
```

```go
switch x := f(); {           // tagless switch = chain of ifs
case x > 100: ...
case x > 10:  ...
default:      ...
}

switch os := runtime.GOOS; os {
case "darwin", "linux": ...   // comma = OR
case "windows":
    fallthrough               // explicit opt-in
default:
}
```

`goto` exists. You will not need it.

## 2.6 Functions

```go
func name(a int, b string) (int, error) { }
func name(a, b int) int { }                     // shared type
func noReturn() { }
```

**Multiple return values** replace out-params, tuples and thrown exceptions:

```go
v, err := strconv.Atoi(s)     // (value, error) — the dominant Go idiom
v, ok := m[key]               // (value, found)  — the "comma ok" idiom
v, ok := i.(Type)             // (value, succeeded)
```

Named results — useful for docs and `defer`, dangerous when overused:

```go
func split(sum int) (x, y int) {
    x = sum * 4 / 9
    y = sum - x
    return          // naked return: fine in a 5-line function, confusing in a 50-line one
}
```

Functions are first class:

```go
func apply(xs []int, f func(int) int) []int {
    out := make([]int, 0, len(xs))
    for _, x := range xs { out = append(out, f(x)) }
    return out
}

double := func(x int) int { return x * 2 }
apply([]int{1, 2}, double)
```

⚠️ There is **no built-in `map`/`filter`/`reduce`** over slices in the classic stdlib.
Write the loop, or use the generic `slices`/`maps` packages (ch. 10). Loops are idiomatic Go.

Closures capture variables **by reference**:

```go
func counter() func() int {
    n := 0
    return func() int { n++; return n }   // n lives on past counter()
}
```

## 2.7 defer

```go
func process(name string) error {
    f, err := os.Open(name)
    if err != nil { return err }
    defer f.Close()          // guaranteed on every return path, and on panic

    mu.Lock()
    defer mu.Unlock()
    ...
}
```

- LIFO: last deferred runs first.
- **Arguments evaluate immediately**, the call runs later:

```go
i := 0
defer fmt.Println(i)   // prints 0
i = 1
```

- Runs at **function** exit, not block exit. Deferring inside a loop leaks until return:

```go
for _, n := range names {
    f, _ := os.Open(n)
    defer f.Close()      // BUG: all files stay open until the function ends
}
// fix: move the body into its own function, or call f.Close() explicitly
```

- With a named result, `defer` can modify the return value:

```go
func do() (err error) {
    defer func() {
        if r := recover(); r != nil { err = fmt.Errorf("panic: %v", r) }
    }()
    ...
}
```

## 2.8 Pointers

```go
x := 42
p := &x        // *int — address of x
*p = 43        // dereference and assign
fmt.Println(x) // 43

var q *int     // nil pointer
if q != nil { }
```

- **No pointer arithmetic.** No `p++`. Pointers are safe references, closer to C#'s
  `ref` than to C pointers.
- No `->`: `p.Field` works on a pointer directly; Go dereferences for you.
- `new(T)` returns `*T` pointing at a zero T. `&T{}` is more common and more useful.
- **You don't manage memory.** Go is garbage collected; returning `&localVar` is safe
  and normal — the compiler moves it to the heap ("escape analysis").

```go
func New() *Config { c := Config{}; return &c }   // perfectly fine in Go
```

## 2.9 Value vs reference semantics

| Type | Assignment / passing copies… |
| --- | --- |
| numbers, bool, string, array, **struct** | the whole value (deep for arrays/structs) |
| pointer, slice, map, chan, func, interface | a small header — the *underlying data is shared* |

```go
type P struct{ X int }
a := P{1}
b := a          // full copy
b.X = 2         // a.X still 1

m := map[string]int{}
n := m          // same map!
n["k"] = 1      // m["k"] == 1
```

This is the single biggest difference from C#/TS/Python, where every object is a reference.

## 2.10 Comments and doc comments

```go
// Package store implements ...      ← package doc, above `package store`
package store

// Get returns the value for key.    ← doc comment: starts with the name
// It returns ErrNotFound if absent.
func Get(key string) ([]byte, error)
```

- `//` and `/* */`. No `///` or docstrings.
- `go doc ./...` and pkg.go.dev render the comment directly above a declaration.
