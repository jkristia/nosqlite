# 10. Generics

Added in Go 1.18. Much less central than in C#/TS — Go code still prefers concrete
types and interfaces. Reach for generics when you'd otherwise write the same function
for `int`, `string` and `T`.

---

## 10.1 Syntax

```go
func Map[T, U any](xs []T, f func(T) U) []U {
    out := make([]U, 0, len(xs))
    for _, x := range xs { out = append(out, f(x)) }
    return out
}

lengths := Map([]string{"a", "bb"}, func(s string) int { return len(s) })
lengths := Map[string, int](names, f)     // explicit, when inference fails
```

Type parameters go in `[...]` before the value parameters. Inference usually works from
the arguments; return types alone are never enough to infer.

## 10.2 Constraints

A constraint is an **interface used as a type set**.

```go
any                       // no constraint
comparable                // supports == and != (map keys, slices.Contains)
cmp.Ordered               // < <= > >= : ints, floats, strings

type Number interface {   // a union of type sets
    ~int | ~int64 | ~float64
}

func Sum[T Number](xs []T) T {
    var total T           // zero value of T
    for _, x := range xs { total += x }
    return total
}
```

- `~int` means "any type whose **underlying** type is int" — so your `type UserID int64`
  satisfies `~int64`. Without `~`, only the exact type matches.
- Constraints can also require **methods**:

```go
type Stringish interface {
    ~string
    Len() int
}
```

`golang.org/x/exp/constraints` has `Integer`, `Float`, `Signed`, etc.; `cmp.Ordered` is
in the stdlib.

## 10.3 Generic types

```go
type Stack[T any] struct {
    items []T
}

func NewStack[T any]() *Stack[T] { return &Stack[T]{} }

func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }

func (s *Stack[T]) Pop() (T, bool) {
    var zero T                              // how you say "default(T)"
    if len(s.items) == 0 { return zero, false }
    v := s.items[len(s.items)-1]
    s.items = s.items[:len(s.items)-1]
    return v, true
}

s := NewStack[int]()
```

```go
type Cache[K comparable, V any] struct {
    mu sync.Mutex
    m  map[K]V
}
```

## 10.4 The hard limitation: no generic methods

**Methods cannot introduce new type parameters.**

```go
func (s *Stack[T]) Map[U any](f func(T) U) *Stack[U]   // ❌ does not compile
```

The receiver's type parameters are all a method gets. Workaround: make it a **function**.

```go
func MapStack[T, U any](s *Stack[T], f func(T) U) *Stack[U]   // ✅
```

This is why Go has no fluent `.Map().Filter()` chains, and why `slices.SortFunc(s, f)`
is a function rather than a method.

## 10.5 Generic stdlib you get for free

```go
slices.Sort, SortFunc, Contains, Index, Clone, Equal, Reverse, Max, Min,
       Compact, Insert, Delete, BinarySearch
maps.Keys, Values, Clone, Equal, DeleteFunc
cmp.Compare, cmp.Or, cmp.Ordered
sync.OnceValue, sync.OnceFunc
atomic.Pointer[T]
```

```go
slices.SortFunc(users, func(a, b User) int {
    return cmp.Or(
        cmp.Compare(a.Last, b.Last),      // first non-zero wins
        cmp.Compare(a.First, b.First),
    )
})
```

## 10.6 When to use them — and when not

Use generics for:
- Container types: `Stack[T]`, `Cache[K,V]`, `Set[T]`, a typed pool.
- Slice/map utilities that are identical for every element type.
- Small helpers like `must[T]`, `ptr[T]`, `zero[T]`, `coalesce[T]`.

Don't use them when:
- **An interface expresses it better.** If you only call methods on the value, take an
  interface — that's the idiomatic answer and it stays simpler.
- You have exactly one instantiation. Write the concrete type.
- You're reaching for reflection-like dynamism. Generics are compile-time only: you
  cannot switch on `T`, construct a `T` from nothing but its name, or read its fields.

Rule of thumb from the Go team: **write the concrete code first; generalize only after
you've written it twice.**

## 10.7 Contrast

| C# / TS | Go |
| --- | --- |
| `class Stack<T>` | `type Stack[T any] struct` |
| `where T : IComparable` | `[T cmp.Ordered]` or a custom constraint interface |
| `where T : new()` | *no equivalent* — pass a factory func |
| `default(T)` / `null` | `var zero T` |
| generic method on a generic class | **not allowed** — use a package-level function |
| covariance / variance | none — `Stack[Dog]` is unrelated to `Stack[Animal]` |
| `typeof(T)` / reflection on T | very limited; no runtime type parameter access |
| overloads | none — different names, or generics |
