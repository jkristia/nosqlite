# 8. Standard library tour

Go's stdlib is large and stable — most projects need almost no dependencies.
`go doc <pkg>` / `go doc <pkg>.<Sym>` reads the docs in your terminal.

---

## 8.1 fmt

```go
fmt.Println("a", 1)                  // spaces + newline
fmt.Printf("%s = %d\n", k, v)
s := fmt.Sprintf("%s=%d", k, v)      // to string
fmt.Fprintf(w, "...", args)          // to an io.Writer
fmt.Errorf("ctx: %w", err)           // to an error
```

| Verb | Prints |
| --- | --- |
| `%v` | default format — works for anything |
| `%+v` | struct **with field names** ← your everyday debug verb |
| `%#v` | Go syntax: `main.User{Name:"a"}` |
| `%T` | the **type** |
| `%d %f %s %t` | int, float, string, bool |
| `%q` | quoted string / rune |
| `%x %X` | hex (works on `[]byte` too) |
| `%p` | pointer address |
| `%w` | wrap an error (`Errorf` only) |
| `%8.2f` `%-10s` | width, precision, left-align |
| `%%` | literal % |

`fmt.Println(v)` calls `v.String()` if the type implements `fmt.Stringer` (ch. 4).

## 8.2 encoding/json

```go
type User struct {
    ID      int64     `json:"id"`
    Name    string    `json:"name,omitempty"`   // omit if zero
    Secret  string    `json:"-"`                // never emitted
    Created time.Time `json:"created"`          // RFC 3339
}

b, err := json.Marshal(u)                  // → []byte
b, err := json.MarshalIndent(u, "", "  ")  // pretty

var u User
err := json.Unmarshal(b, &u)               // POINTER required

json.NewEncoder(w).Encode(u)               // straight to an io.Writer (adds \n)
json.NewDecoder(r).Decode(&u)              // straight from an io.Reader — use for HTTP
```

Points that trip people up:
- **Only exported fields** are marshaled. A lowercase field silently disappears.
- Unmarshal is **case-insensitive** on field matching, and **ignores unknown keys**
  (use `dec.DisallowUnknownFields()` to make them errors).
- Missing JSON key → field left at its zero value. To tell "absent" from "zero", use a
  pointer (`*string`) or `json.RawMessage`.
- `nil` slice → `null`; empty slice `[]T{}` → `[]`.
- Schemaless JSON decodes into `any` with these fixed types:

```go
var v any
json.Unmarshal(b, &v)
// object → map[string]any, array → []any, number → float64 (!), string → string,
// bool → bool, null → nil
```

`float64` for every number is the classic surprise — large int64 ids lose precision.
Use `json.Number` via a decoder with `UseNumber()` when that matters.

Custom encoding:

```go
func (c Celsius) MarshalJSON() ([]byte, error)   { ... }
func (c *Celsius) UnmarshalJSON(b []byte) error  { ... }
```

## 8.3 os — files, env, args

```go
data, err := os.ReadFile(path)                 // whole file → []byte
err := os.WriteFile(path, data, 0o644)         // create/truncate

f, err := os.Open(path)                        // read-only
f, err := os.Create(path)                      // create/truncate, write
f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
defer f.Close()

os.Remove(path); os.Rename(a, b)
os.MkdirAll(dir, 0o755)
os.Stat(path)                                  // (FileInfo, error)
if errors.Is(err, os.ErrNotExist) { }          // the right existence check
os.ReadDir(dir)                                // []DirEntry

os.Getenv("KEY"); os.LookupEnv("KEY")          // second returns (string, bool)
os.Args                                        // [prog, arg1, ...]
os.Exit(1)                                     // ⚠️ skips all defers
os.Stdin / os.Stdout / os.Stderr               // *os.File, i.e. io.Reader/Writer
os.MkdirTemp("", "pfx"); os.CreateTemp("", "pfx")
```

Durable write (avoid a torn file on crash): write to a temp file in the same directory,
`f.Sync()`, `f.Close()`, then `os.Rename` over the target.

`path/filepath` for paths (OS-aware separators): `Join`, `Base`, `Dir`, `Ext`, `Abs`,
`Clean`, `WalkDir`. Use `path` only for URLs/slash paths.

## 8.4 io & bufio

```go
type Reader interface{ Read(p []byte) (int, error) }   // io.EOF ends the stream
type Writer interface{ Write(p []byte) (int, error) }

io.Copy(dst, src)                    // stream, no full buffering
io.ReadAll(r)                        // → []byte (careful with size)
io.WriteString(w, s)
io.MultiWriter(a, b)                 // tee
io.LimitReader(r, n)
strings.NewReader(s); bytes.NewReader(b); bytes.NewBuffer(nil)
```

Everything speaks these two interfaces: files, network conns, HTTP bodies, gzip,
buffers, `strings.Builder`. Write your own functions against `io.Reader`/`io.Writer`
rather than `*os.File` and they become testable with a `strings.Reader`.

```go
sc := bufio.NewScanner(f)            // line-by-line
for sc.Scan() { line := sc.Text() }
if err := sc.Err(); err != nil { }   // ⚠️ Scan()==false could mean error, not EOF

w := bufio.NewWriter(f)              // batch small writes
defer w.Flush()                      // ⚠️ forget this and you lose data
```

`Scanner` has a 64 KB line limit by default — `sc.Buffer(...)` to raise it, or use
`bufio.Reader.ReadString('\n')`.

## 8.5 strconv

```go
strconv.Atoi("42")                       // (int, error)
strconv.Itoa(42)
strconv.ParseInt("ff", 16, 64)           // base, bit size
strconv.ParseFloat("1.5", 64)
strconv.ParseBool("true")
strconv.FormatInt(n, 10)
strconv.Quote(s)
```

Faster and clearer than `fmt.Sprintf` for single values.

## 8.6 sort / slices / maps

```go
// Go 1.21+ generics — prefer these
slices.Sort(nums)
slices.SortFunc(users, func(a, b User) int { return cmp.Compare(a.Age, b.Age) })
slices.SortStableFunc(...)
slices.BinarySearch(sorted, x)
slices.Contains / Index / Reverse / Equal / Clone / Max / Min / Compact
maps.Keys(m) / maps.Values(m)            // iterators (Go 1.23+); slices.Collect(...) to materialize

// classic
sort.Ints(nums); sort.Strings(ss); sort.Float64s(fs)
sort.Slice(users, func(i, j int) bool { return users[i].Age < users[j].Age })
sort.SearchInts(nums, x)
```

Note `SortFunc` wants a **three-way comparison** (`-1/0/1`, use `cmp.Compare`), while
`sort.Slice` wants a **less** predicate (`bool`).

## 8.7 time

```go
now := time.Now()
t := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
d := 500 * time.Millisecond          // Duration is an int64 of nanoseconds
time.Sleep(d)

now.Add(time.Hour)
now.Sub(then)                        // → Duration
now.Before(t) / After(t) / Equal(t)  // ⚠️ use Equal, not ==, for Time
time.Since(start)                    // idiomatic elapsed
d.Seconds(); d.String()              // "1.5s"

t.Format("2006-01-02 15:04:05")      // the reference layout, not yyyy-MM-dd
time.Parse(time.RFC3339, s)
t.UTC(); t.Local(); t.Unix(); t.UnixMilli()

timer := time.NewTimer(d);  <-timer.C
tick  := time.NewTicker(d); defer tick.Stop()   // ⚠️ leaks if not stopped
```

The magic layout numbers are a mnemonic: `01/02 03:04:05PM '06 -0700` = month, day,
hour, minute, second, year, zone.

## 8.8 flag — CLI arguments

```go
var (
    verbose = flag.Bool("v", false, "verbose output")
    port    = flag.Int("port", 8080, "listen port")
    name    string
)
func main() {
    flag.StringVar(&name, "name", "world", "who to greet")
    flag.Parse()
    fmt.Println(*port, name, flag.Args())   // Args() = positional leftovers
}
```

Subcommands: `flag.NewFlagSet("sub", flag.ExitOnError)` + a switch on `os.Args[1]`.

## 8.9 log & log/slog

```go
log.Printf("starting on %d", port)
log.Fatal(err)          // log + os.Exit(1) — main only, skips defers
log.Panic(err)          // log + panic

slog.Info("request", "method", m, "dur", d)          // structured, Go 1.21+
slog.Error("failed", "err", err)
h := slog.NewJSONHandler(os.Stdout, nil)
slog.SetDefault(slog.New(h))
```

Libraries should not log — return errors and let the caller decide.

## 8.10 net/http (both ends in 20 lines)

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")                     // Go 1.22+ routing
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"id": id})
})
http.ListenAndServe(":8080", mux)
```

```go
req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
resp, err := http.DefaultClient.Do(req)
if err != nil { return err }
defer resp.Body.Close()                    // ⚠️ always, or you leak connections
var out Payload
err = json.NewDecoder(resp.Body).Decode(&out)
```

Use a `&http.Client{Timeout: 10*time.Second}` — the default client has **no timeout**.

## 8.11 Others worth knowing they exist

| Package | For |
| --- | --- |
| `bytes` | `strings` API over `[]byte`; `bytes.Buffer` |
| `encoding/binary` | fixed-width ints ↔ bytes, endianness — binary file formats |
| `encoding/hex`, `encoding/base64`, `encoding/csv` | encodings |
| `crypto/sha256`, `crypto/rand` | hashing; **cryptographically secure** randomness |
| `math/rand/v2` | fast non-crypto randomness |
| `hash/fnv`, `hash/crc32` | cheap checksums |
| `regexp` | RE2 — linear time, no backtracking or lookaround |
| `text/template`, `html/template` | templating (`html/` auto-escapes) |
| `reflect` | runtime type inspection — powers `json`; avoid otherwise |
| `unsafe` | pointer casts; needed for cgo edges, otherwise don't |
| `runtime`, `runtime/pprof` | GC stats, profiling |
| `embed` | `//go:embed` files into the binary |
| `iter` | user-defined range-over-func iterators (Go 1.23+) |
| `testing`, `testing/quick` | see ch. 9 |
| `sync`, `context` | see ch. 7 |
