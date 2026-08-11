# 4. Interfaces

Go's only polymorphism. Small, implicit, and used differently than C#/TS interfaces.

---

## 4.1 Implicit satisfaction

```go
type Speaker interface {
    Speak() string
}

type Dog struct{}
func (d Dog) Speak() string { return "woof" }   // no `implements` anywhere

var s Speaker = Dog{}    // it just works
```

- **Structural typing**, like TypeScript — but checked at compile time on assignment.
- The implementing type doesn't import the interface, doesn't know it exists.
- Consequence: you can define an interface for a type you don't own, *after the fact*.

## 4.2 Declare interfaces at the consumer

The C# habit is `IUserRepository` next to `UserRepository`. Go inverts this:

```go
// package report — the CONSUMER declares exactly what it needs
type userFetcher interface {
    FetchUser(id int64) (*User, error)
}

func Generate(f userFetcher) error { ... }
```

Why: the producer package stays dependency-free, the consumer's needs stay minimal, and
tests can pass a 3-line fake.

Rules of thumb:
- **"Accept interfaces, return structs."** Take an interface as a parameter; return the
  concrete `*Client`.
- The bigger the interface, the weaker the abstraction. 1–3 methods is normal.
- Don't create an interface until you have a second implementation *or* a test that needs one.
- No `I` prefix. Name it for the behaviour: `Reader`, `Store`, `userFetcher`.

## 4.3 The standard interfaces worth memorizing

```go
type error interface{ Error() string }                        // ch. 6
type Stringer interface{ String() string }                    // fmt — how %v prints
type Reader interface{ Read(p []byte) (int, error) }          // io
type Writer interface{ Write(p []byte) (int, error) }         // io
type Closer interface{ Close() error }                        // io
type ReadWriteCloser interface{ Reader; Writer; Closer }      // embedded composition
type Marshaler interface{ MarshalJSON() ([]byte, error) }     // encoding/json
type Unmarshaler interface{ UnmarshalJSON([]byte) error }
type Sorter interface{ Len() int; Less(i,j int) bool; Swap(i,j int) }  // sort
```

Implement `String()` on your named types and `fmt.Println` formats them for free —
Go's `ToString()`.

```go
func (l Level) String() string {
    return [...]string{"debug", "info", "warn", "error"}[l]
}
```

## 4.4 Interface composition

```go
type Reader interface{ Read([]byte) (int, error) }
type Closer interface{ Close() error }

type ReadCloser interface {   // embed, don't re-list
    Reader
    Closer
}
```

## 4.5 `any` and type switches

```go
var v any = 42          // any == interface{}, the empty interface: matched by everything
```

Getting the concrete value back out:

```go
s := v.(string)          // type assertion — PANICS if wrong
s, ok := v.(string)      // comma-ok — safe, ok=false and s="" if wrong

switch x := v.(type) {   // type switch
case nil:            ...
case string:         fmt.Println(len(x))   // x is a string here
case int, int64:     ...                   // x stays `any` in a multi-type case
case error:          ...                   // interfaces work as cases too
case fmt.Stringer:   ...
default:             ...
}
```

Assertions also work interface→interface:

```go
if c, ok := r.(io.Closer); ok { c.Close() }   // "does this Reader also close?"
```

Use `any` sparingly — it's `object`/`unknown`. Generics (ch. 10) are usually better now.

## 4.6 The nil-interface trap

The one that costs everyone an afternoon:

```go
type MyErr struct{}
func (e *MyErr) Error() string { return "boom" }

func f() error {
    var p *MyErr = nil    // a nil POINTER
    return p              // wrapped into a NON-nil interface
}

f() != nil   // → TRUE. Surprise.
```

- An interface value is a pair **(type, value)**. It is `nil` only when *both* are nil.
  Here the type is `*MyErr`, so the interface isn't nil.
- **Fix:** never declare a typed nil and return it. Return a literal `nil`:

```go
func f() error {
    if ok { return nil }
    return &MyErr{}
}
```

## 4.7 Compile-time conformance check

Since there's no `implements`, assert it explicitly so a broken implementation fails at
build time rather than at the call site:

```go
var _ io.Reader = (*MyFile)(nil)   // costs nothing at runtime
var _ Speaker   = Dog{}
```

## 4.8 Costs

- Interface calls are dynamic dispatch — slightly slower than direct calls, and they
  usually block inlining.
- Putting a value in an interface may **heap-allocate** it.
- Irrelevant in 99% of code; relevant in a hot inner loop.

## 4.9 Contrast table

| C# / TS | Go |
| --- | --- |
| `class C : IFoo` (explicit) | nothing — matching methods are enough |
| `IFoo` naming | `Foo`, `Fooer` |
| interface next to implementation | interface next to the *caller* |
| big service interfaces | 1–3 method interfaces |
| `object` / `unknown` | `any` |
| `as` / pattern matching | comma-ok assertion / type switch |
| abstract base class | interface + composition (no partial impl, except embedding) |
| generic constraints via interfaces | same, plus type-set constraints (ch. 10) |
