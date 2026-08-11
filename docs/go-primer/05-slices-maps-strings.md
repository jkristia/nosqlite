# 5. Slices, maps, strings, bytes

Go's collection story is deliberately tiny: **arrays, slices, maps**. No `List<T>`,
no `Dictionary` class, no LINQ. Understanding slice aliasing is mandatory — it is the
#1 source of real bugs for newcomers.

---

## 5.1 Arrays (rarely used directly)

```go
var a [3]int              // [0 0 0] — length is PART OF THE TYPE
b := [3]int{1, 2, 3}
c := [...]string{"x","y"} // length inferred = 2
len(a)                    // 3, always
```

- `[3]int` and `[4]int` are **different types**.
- Arrays are **values**: assigning or passing copies all elements.
- Use them for fixed-size buffers (`var buf [4096]byte`) and hash keys. Otherwise: slices.

## 5.2 Slices — the everyday list

A slice is a 3-word header: **pointer to an array, length, capacity**.

```go
var s []int                   // nil slice: len 0, cap 0 — append works
s := []int{1, 2, 3}
s := make([]int, 5)           // len 5, [0 0 0 0 0]
s := make([]int, 0, 100)      // len 0, cap 100 — preallocate when size is known
s = append(s, 4)
s = append(s, other...)       // spread
len(s); cap(s)
clear(s)                      // zero all elements (Go 1.21+)
```

⚠️ `append` **returns** a new header — always reassign. `append(s, x)` alone is a bug
(`go vet` catches it).

### Slicing shares memory

```go
s := []int{1, 2, 3, 4, 5}
t := s[1:3]          // [2 3]  — half-open, len 2, cap 4
t[0] = 99            // s is now [1 99 3 4 5]  ← same backing array
```

```go
s[low:high]        // len = high-low
s[:n] / s[n:] / s[:]
s[low:high:max]    // 3-index: also caps capacity — use to prevent append aliasing
```

### The append-aliasing bug

```go
a := []int{1, 2, 3, 4, 5}
b := a[:2]           // len 2, cap 5
b = append(b, 99)    // fits in cap → writes into a's array
// a == [1 2 99 4 5]  ← a was mutated
```

Rules:
- If `cap` is sufficient, `append` writes **in place** and returns the same array.
- If not, it **allocates a new array** and copies — the alias silently breaks.
- So whether callers see your mutation depends on capacity. Never rely on it.

Defensive copy:

```go
dst := make([]int, len(src))
copy(dst, src)
// or
dst := append([]int(nil), src...)
// or (Go 1.21+)
dst := slices.Clone(src)
```

⚠️ Same trap when passing slices to functions: the function can mutate your elements
(shared array) but cannot change your `len` (its header is a copy).

### Common operations

```go
s = append(s[:i], s[i+1:]...)         // delete index i (order kept)
s[i] = s[len(s)-1]; s = s[:len(s)-1]  // delete fast (order lost)
s = append(s[:i], append([]int{x}, s[i:]...)...)  // insert (or slices.Insert)
s = s[:0]                              // truncate, keep capacity — reuse in loops

// Go 1.21+ stdlib
slices.Contains(s, x)
slices.Index(s, x)
slices.Sort(s)
slices.SortFunc(s, func(a, b T) int { return cmp.Compare(a.N, b.N) })
slices.Reverse(s)
slices.Equal(a, b)
slices.Max(s) / slices.Min(s)
```

### nil vs empty

```go
var a []int          // nil:   a == nil, len 0
b := []int{}         // empty: b != nil, len 0
```

Both work with `len`, `range`, `append`. Difference matters only for `== nil` checks and
JSON (`null` vs `[]`). **Prefer `var a []int`** as the zero value.

## 5.3 Maps

```go
m := map[string]int{"a": 1}
m := make(map[string]int)
m := make(map[string]int, 100)   // size hint

m["b"] = 2
v := m["missing"]                 // 0 — zero value, NO error
v, ok := m["missing"]             // ok == false ← use this to distinguish
delete(m, "a")
len(m)
clear(m)                          // Go 1.21+
```

⚠️ Traps:

```go
var m map[string]int    // nil map
_ = m["x"]              // ok — reads return zero
m["x"] = 1              // PANIC: assignment to entry in nil map
```

```go
for k, v := range m { }  // ORDER IS RANDOMIZED on purpose, every run
```

Deterministic iteration:

```go
keys := make([]string, 0, len(m))
for k := range m { keys = append(keys, k) }
slices.Sort(keys)
for _, k := range keys { use(k, m[k]) }
// or: for _, k := range slices.Sorted(maps.Keys(m))   // Go 1.23+
```

```go
type P struct{ X int }
m := map[string]P{"a": {}}
m["a"].X = 1            // compile error: not addressable
// use map[string]*P instead
```

- Key type must be **comparable** (no slice/map/func keys). Structs of comparable
  fields are fine.
- Maps are **not safe for concurrent use**. Concurrent read+write → runtime crash.
  Guard with `sync.Mutex`/`RWMutex`, or use `sync.Map` for a few specific patterns.

**Sets**: no set type. Use `map[T]struct{}` (zero bytes per entry) or `map[T]bool`:

```go
seen := map[string]struct{}{}
seen["a"] = struct{}{}
if _, ok := seen["a"]; ok { }
```

## 5.4 Strings, bytes, runes

```go
s := "héllo"
len(s)          // 6 — BYTES, not characters
s[0]            // 104, a byte (uint8) — not "h"
s[1]            // 195 — half of é. Indexing is byte indexing.
```

- A `string` is an **immutable read-only slice of bytes**, conventionally UTF-8.
- `s[i] = 'x'` does not compile. Build new strings instead.

```go
for i, r := range s {         // decodes UTF-8: i = byte offset, r = rune
    fmt.Println(i, string(r))
}
utf8.RuneCountInString(s)     // 5 — actual character count
[]rune(s)                     // convert to code points, then index safely
[]byte(s)                     // copy to a mutable byte slice
string(bs)                    // back to a string (copies)
```

| Literal | Type | Notes |
| --- | --- | --- |
| `"hi"` | string | escapes processed |
| `` `hi\n` `` | string | raw — no escapes, can span lines; used for regex, JSON, struct tags |
| `'h'` | rune (int32) | single quotes are **not** strings |

### Building strings

```go
a + b                       // fine for a couple
strings.Join(parts, ",")    // fine for a known slice

var sb strings.Builder      // fine for a loop — the right answer
for _, p := range parts { sb.WriteString(p) }
s := sb.String()
```

Concatenating in a loop with `+=` is O(n²) — every `+` allocates a new string.

### strings toolbox

```go
strings.Contains, HasPrefix, HasSuffix, Index, LastIndex
strings.Split, SplitN, Fields          // Fields splits on any whitespace
strings.Join, Repeat, Replace, ReplaceAll
strings.TrimSpace, Trim, TrimPrefix, TrimSuffix, TrimLeft/Right
strings.ToUpper, ToLower, EqualFold    // EqualFold = case-insensitive ==
strings.Cut(s, sep) (before, after string, found bool)   // the modern split-in-two
strings.NewReader(s)                   // string → io.Reader
```

`bytes` mirrors `strings` for `[]byte` — same function names.

### When to use []byte vs string

- `string` — text you pass around, map keys, comparisons.
- `[]byte` — I/O, mutation, building buffers. `io.Reader`/`Writer` speak `[]byte`.
- Converting between them **copies**. In a hot loop, pick one and stay there.
