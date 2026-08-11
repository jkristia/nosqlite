# 9. Testing and tooling

Testing is built into the toolchain — no NUnit/xUnit, no Jest, no pytest, and
deliberately **no assertion library** in the stdlib.

---

## 9.1 The rules

- File must end in `_test.go`. It is excluded from normal builds.
- Function must be `func TestXxx(t *testing.T)` — exported-style name, one arg.
- Lives in the **same package** (white-box, sees unexported members), or in
  `package foo_test` (black-box, only the public API). Both may sit in the same folder.
- Fail with `t.Error*` (continue) or `t.Fatal*` (stop this test now).

```go
package store

import "testing"

func TestGet(t *testing.T) {
    s := New()
    s.Put("k", "v")

    got, err := s.Get("k")
    if err != nil {
        t.Fatalf("Get() error = %v", err)      // Fatal: nothing below makes sense
    }
    if got != "v" {
        t.Errorf("Get() = %q, want %q", got, "v")
    }
}
```

Message convention: **`got, want`**. `Thing() = <got>, want <want>`.

## 9.2 Table-driven tests — the dominant Go style

```go
func TestParse(t *testing.T) {
    tests := []struct {
        name    string
        in      string
        want    int
        wantErr bool
    }{
        {"simple", "42", 42, false},
        {"negative", "-1", -1, false},
        {"garbage", "abc", 0, true},
        {"empty", "", 0, true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {     // subtest: own name, own failure
            got, err := Parse(tt.in)
            if (err != nil) != tt.wantErr {
                t.Fatalf("Parse(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
            }
            if got != tt.want {
                t.Errorf("Parse(%q) = %d, want %d", tt.in, got, tt.want)
            }
        })
    }
}
```

Run one case: `go test -run 'TestParse/garbage' ./...`

## 9.3 The testing.T API

```go
t.Errorf(...) / t.Error(...)      // mark failed, keep going
t.Fatalf(...) / t.Fatal(...)      // mark failed, return now (⚠️ NOT from a goroutine)
t.Logf(...)                       // shown only with -v or on failure
t.Skip("reason") / t.SkipNow()
if testing.Short() { t.Skip("slow") }   // pairs with `go test -short`
t.Helper()                        // report the CALLER's line, not this one
t.Cleanup(func(){ ... })          // like defer, but also runs after subtests
t.TempDir()                       // auto-removed temp directory
t.Setenv("K","V")                 // auto-restored env var (disables t.Parallel)
t.Parallel()                      // run this test alongside other parallel ones
t.Context()                       // context cancelled when the test ends (Go 1.24+)
```

A shared assertion helper — this is how Go does "assert libraries":

```go
func mustEqual[T comparable](t *testing.T, got, want T) {
    t.Helper()                                  // failures point at the caller
    if got != want { t.Fatalf("got %v, want %v", got, want) }
}
```

## 9.4 Setup, fixtures, golden files

```go
func TestMain(m *testing.M) {      // package-level setup/teardown, ONE per package
    setup()
    code := m.Run()
    teardown()
    os.Exit(code)
}
```

- `testdata/` is ignored by the Go tool — put fixtures there.
- Golden files: compare output to `testdata/x.golden`, with an `-update` flag to rewrite.

```go
var update = flag.Bool("update", false, "update golden files")
```

- Temp dirs/files: `t.TempDir()`, cleaned automatically.
- Fakes: just define a struct implementing the consumer's small interface (ch. 4).
  Go has no built-in mocking framework and rarely needs one.

## 9.5 Comparing values

```go
got == want                     // comparable types only
bytes.Equal(a, b)
strings.EqualFold(a, b)
slices.Equal(a, b)              // Go 1.21+
maps.Equal(a, b)
reflect.DeepEqual(a, b)         // last resort: slow, and NaN/nil-vs-empty surprises
```

For deep structural diffs, the near-universal third-party choice is
`github.com/google/go-cmp/cmp` — `cmp.Diff(want, got)` prints a readable diff.

## 9.6 Benchmarks

```go
func BenchmarkParse(b *testing.B) {
    for b.Loop() {              // Go 1.24+; older: for i := 0; i < b.N; i++
        Parse("42")
    }
}

func BenchmarkX(b *testing.B) {
    data := setup()             // not timed if you reset:
    b.ResetTimer()
    b.ReportAllocs()
    for b.Loop() { work(data) }
}
```

```bash
go test -bench=. -benchmem ./...
go test -bench=Parse -count=10 ./... > new.txt   # then: benchstat old.txt new.txt
```

Output: `BenchmarkParse-10  50000000  23.4 ns/op  16 B/op  1 allocs/op`
→ iterations, time each, bytes allocated, allocation count. `allocs/op` is usually the
number to attack first.

⚠️ The compiler can delete work whose result you discard. Assign to a package-level
`var sink` to keep it honest.

## 9.7 Fuzzing

```go
func FuzzParse(f *testing.F) {
    f.Add("42")                     // seed corpus
    f.Fuzz(func(t *testing.T, s string) {
        _, _ = Parse(s)             // must not panic
    })
}
```

```bash
go test -fuzz=FuzzParse -fuzztime=30s
```

Failing inputs are written to `testdata/fuzz/` and become permanent regression tests.

## 9.8 Examples — tests that are also documentation

```go
func ExampleParse() {
    v, _ := Parse("42")
    fmt.Println(v)
    // Output: 42
}
```

`go test` runs it and compares stdout to the `// Output:` comment. It also appears in
the rendered docs.

## 9.9 Commands

```bash
go test ./...                        # everything
go test -v ./pkg                     # verbose
go test -run 'TestParse/empty' ./... # regex on test/subtest name
go test -race ./...                  # data-race detector — run in CI
go test -count=1 ./...               # bypass the result cache
go test -short ./...                 # skip tests guarded by testing.Short()
go test -timeout 30s ./...           # default is 10m
go test -failfast ./...
go test -shuffle=on ./...            # catch order dependencies

go test -cover ./...
go test -coverprofile=c.out ./... && go tool cover -html=c.out

go test -cpuprofile=cpu.out -memprofile=mem.out -bench=.
go tool pprof -http=:8080 cpu.out
```

Results are **cached**: an unchanged package prints `(cached)`. `-count=1` forces a rerun.

## 9.10 Build & quality tooling

```bash
gofmt -l -w .        # format. Not optional, not configurable, no bikeshedding.
goimports -w .       # gofmt + fix the import block
go vet ./...         # printf mismatches, copied locks, unreachable code, bad tags
go build ./...
go run ./cmd/app
go install ./cmd/app          # binary into $GOBIN (~/go/bin)
go clean -cache -testcache
go doc net/http Client        # docs in the terminal
go env GOPATH GOMODCACHE
```

Cross compilation is one line — no toolchain install:

```bash
GOOS=linux GOARCH=amd64 go build -o app ./cmd/app
GOOS=windows GOARCH=amd64 go build -o app.exe ./cmd/app
```
(unless the package uses cgo — see ch. 12)

Smaller binaries and embedded version info:

```bash
go build -ldflags="-s -w" ./cmd/app
go build -ldflags="-X main.version=1.2.3" ./cmd/app
```

Linting beyond `vet`: `staticcheck` (excellent, focused) or `golangci-lint`
(aggregator). Neither ships with Go.

## 9.11 A Makefile most Go projects converge on

```make
.PHONY: build test lint fmt
build:  ; go build ./...
test:   ; go test -race ./...
cover:  ; go test -coverprofile=c.out ./... && go tool cover -html=c.out
lint:   ; go vet ./... && staticcheck ./...
fmt:    ; gofmt -l -w . && go mod tidy
```
