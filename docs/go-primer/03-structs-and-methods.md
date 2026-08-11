# 3. Structs, methods, receivers — Go's answer to classes

There is no `class`. You get **a data type** and **functions attached to it**, declared
separately. That separation is the whole trick.

---

## 3.1 Declaring and creating

```go
type Server struct {
    Host    string
    Port    int
    timeout time.Duration   // unexported
}
```

```go
s := Server{Host: "localhost", Port: 8080}  // keyed literal — always use this form
s := Server{"localhost", 8080, 0}           // positional — brittle, avoid
p := &Server{Host: "localhost"}             // *Server, the usual way to "new up"
var z Server                                // zero value: "", 0, 0 — often usable as-is
p := new(Server)                            // *Server to a zero value; &Server{} is preferred
```

- Omitted fields get their zero value. There is no constructor call, no `new` keyword needed.
- **Design for a useful zero value.** `var buf bytes.Buffer` and `var mu sync.Mutex` work
  with no initialization at all. That's idiomatic Go.

## 3.2 Constructors

Go has none. Convention is a package-level `New…` function:

```go
func NewServer(host string, port int) *Server {
    return &Server{Host: host, Port: port, timeout: 30 * time.Second}
}

func NewServer(host string) (*Server, error) {   // when it can fail
    if host == "" { return nil, errors.New("host required") }
    return &Server{Host: host}, nil
}
```

- Name it `New` if the package exports one main type (`store.New()`), otherwise
  `NewX` (`store.NewReader()`).
- For many optional params, the "functional options" pattern replaces overloads
  (Go has **no method overloading** and **no default parameters**):

```go
type Option func(*Server)

func WithPort(p int) Option    { return func(s *Server) { s.Port = p } }
func WithTimeout(d time.Duration) Option { return func(s *Server) { s.timeout = d } }

func NewServer(host string, opts ...Option) *Server {
    s := &Server{Host: host, Port: 80}
    for _, o := range opts { o(s) }
    return s
}

srv := NewServer("localhost", WithPort(8080), WithTimeout(time.Second))
```

## 3.3 Methods and receivers

```go
func (s *Server) Start() error { ... }
//    ^^^^^^^^^ the receiver — Go's `this` / `self`, but explicitly named and typed
```

- The name is yours: `s`, `srv`, `db`. Convention is **1–2 letters**, consistent across
  all methods of the type. Never `this` or `self`.
- Methods live at package level, outside the struct body, and may sit in any file of
  the package.
- You can attach methods to **any named type you declared**, not just structs:

```go
type Celsius float64
func (c Celsius) String() string { return fmt.Sprintf("%.1f°C", float64(c)) }

type IDs []int64
func (ids IDs) Contains(x int64) bool { ... }
```

- You **cannot** add methods to types from other packages (no C# extension methods).
  Workaround: define your own named type wrapping it.

## 3.4 Value vs pointer receiver — the decision

```go
func (s Server) Describe() string { return s.Host }   // gets a COPY
func (s *Server) SetPort(p int)   { s.Port = p }      // can mutate the original
```

| Use `*T` when | Use `T` when |
| --- | --- |
| The method mutates the receiver | The type is small and you want copy semantics |
| The struct is large (avoid copying) | The type is a simple scalar/alias (`Celsius`) |
| The type contains a `sync.Mutex` (copying a lock is a bug) | The value is genuinely immutable |
| **Any other method already uses `*T`** | |

**Be consistent within a type** — mixing receivers is a common review comment and
causes interface-satisfaction surprises (see 3.7).

Calling is transparent — Go inserts `&` / `*` automatically for *addressable* values:

```go
var s Server
s.SetPort(80)     // → (&s).SetPort(80)
p := &s
p.Describe()      // → (*p).Describe()
```

Not addressable → no auto-address:

```go
m := map[string]Server{"a": {}}
m["a"].SetPort(80)   // compile error: map element is not addressable
```

## 3.5 Struct tags

Metadata strings read via reflection — Go's equivalent of C# attributes / TS decorators,
but only strings:

```go
type User struct {
    ID    int64  `json:"id"`
    Name  string `json:"name,omitempty"`
    Token string `json:"-"`                     // never serialized
    Age   int    `json:"age" db:"user_age"`     // multiple consumers
}
```

Backticks matter (raw string). Typos are silent — `go vet` catches malformed tags.

## 3.6 Embedding — composition, not inheritance

```go
type Base struct{ ID int64 }
func (b *Base) Describe() string { return fmt.Sprint(b.ID) }

type User struct {
    Base            // embedded: no field name
    Name string
}

u := User{Base: Base{ID: 1}, Name: "Ada"}
u.ID                // promoted field   → u.Base.ID
u.Describe()        // promoted method  → u.Base.Describe()
```

Looks like inheritance. **It is not**:

- No virtual dispatch. `Base.Describe()` cannot call a `User` override — it only ever
  sees `Base`. There is no `base.`/`super.`, no `virtual`/`override`.
- Shadowing is allowed: define `func (u *User) Describe()` and `u.Describe()` picks it,
  but anything holding a `*Base` still gets `Base`'s.
- Ambiguous promotions from two embedded types are a compile error at the call site;
  disambiguate with `u.Base.Describe()`.

Embed an **interface** to get a partial implementation cheaply:

```go
type LoggingStore struct {
    Store             // interface — all methods forwarded
    log *slog.Logger
}
func (s LoggingStore) Get(k string) ([]byte, error) {   // override just one
    s.log.Info("get", "key", k)
    return s.Store.Get(k)
}
```

Prefer a **named field** when you don't want promotion — that's plain composition and
usually clearer:

```go
type Service struct {
    store  *Store       // explicit: s.store.Get(...)
    tracer *Tracer
}
```

## 3.7 Method sets (the rule behind most interface errors)

- Methods on `T` belong to both `T` and `*T`.
- Methods on `*T` belong to **only `*T`**.

```go
type Stringer interface{ String() string }
type Foo struct{}
func (f *Foo) String() string { return "foo" }

var s Stringer = Foo{}    // ERROR: Foo does not implement Stringer
var s Stringer = &Foo{}   // ok
```

Read the error as: "you gave a value, the method needs a pointer."

## 3.8 Comparability & copying

```go
a == b                  // legal if all fields are comparable
```

- Structs of comparable fields are `==`-comparable and usable as **map keys**.
- A struct containing a slice, map or func is **not** comparable — `==` won't compile.
- Deep compare: `reflect.DeepEqual(a, b)` (slow, tests only).
- Copying a struct is **shallow**: pointer/slice/map fields still share the same data.

## 3.9 Anonymous structs

Handy for table tests and one-off payloads:

```go
tests := []struct {
    name string
    in   int
    want int
}{
    {"zero", 0, 0},
    {"one", 1, 2},
}

json.NewEncoder(w).Encode(struct {
    OK bool `json:"ok"`
}{true})
```

## 3.10 Mental translation table

| C# / TypeScript | Go |
| --- | --- |
| `class Foo { }` | `type Foo struct { }` |
| `public int X` | `X int` |
| `private int x` | `x int` (package-visible, not type-private) |
| `this` | the receiver variable you named |
| `new Foo(a, b)` | `NewFoo(a, b)` returning `*Foo` |
| constructor overloads / default args | functional options, or several `NewX` funcs |
| `class B : A` (inheritance) | embedding (no polymorphism) or a plain field |
| `virtual` / `override` | an **interface** field |
| `interface IFoo` + `: IFoo` | `type Foo interface` — satisfied implicitly |
| `[Attribute]` / `@decorator` | struct tags (strings only) |
| static method | plain package-level function |
| static field | package-level `var` |
| `partial class` | several files in the same package |
| extension method | not possible on foreign types |
