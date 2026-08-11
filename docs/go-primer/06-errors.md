# 6. Errors, panic, recover

**There are no exceptions.** Errors are values, returned as the last result, and you
handle them at every call. That's why Go code has so many `if err != nil` blocks — it's
a deliberate trade: verbosity for explicit control flow.

---

## 6.1 The interface

```go
type error interface {
    Error() string
}
```

Anything with `Error() string` is an error.

## 6.2 The basic pattern

```go
func ReadConfig(path string) (*Config, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, err              // pass it up
    }
    var c Config
    if err := json.Unmarshal(data, &c); err != nil {
        return nil, fmt.Errorf("parsing %s: %w", path, err)   // add context
    }
    return &c, nil
}
```

Rules:
- `error` is always the **last** return value.
- Check it **immediately**. Don't stack calls and check later.
- On error, other return values are meaningless — return the zero value alongside.
- Handle it **once**: either log it or return it, not both.

## 6.3 Creating errors

```go
errors.New("something failed")                  // static
fmt.Errorf("user %d not found", id)             // formatted
fmt.Errorf("loading %s: %w", path, err)         // WRAPPED — keeps the cause
```

`%w` vs `%v`: `%w` preserves the original error for `errors.Is/As`; `%v` flattens it to
text. Wrap when the caller might want to inspect the cause.

**Message style:** lowercase, no trailing punctuation, no "error:" prefix — they get
concatenated: `"loading config: parsing json: unexpected EOF"`.

## 6.4 Sentinel errors

Package-level values callers can compare against — Go's "exception type" for simple cases:

```go
var ErrNotFound = errors.New("not found")

func Get(k string) ([]byte, error) {
    ...
    return nil, ErrNotFound
}

// caller
if errors.Is(err, ErrNotFound) { ... }
```

`errors.Is` unwraps the whole `%w` chain — `==` does not. Always prefer `errors.Is`.

Familiar stdlib sentinels: `io.EOF`, `os.ErrNotExist`, `sql.ErrNoRows`, `context.Canceled`,
`context.DeadlineExceeded`.

## 6.5 Custom error types

When the caller needs **data**, not just identity:

```go
type ValidationError struct {
    Field string
    Msg   string
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("field %s: %s", e.Field, e.Msg)
}

// caller
var ve *ValidationError
if errors.As(err, &ve) {
    fmt.Println(ve.Field)        // typed access, through wrapping
}
```

- `errors.As` = "is there anything of this type in the chain?" — the type-based
  counterpart of `errors.Is`. Pass a **pointer to** the target variable.
- Implement `Unwrap() error` to make your type participate in the chain:

```go
type OpError struct{ Op string; Err error }
func (e *OpError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *OpError) Unwrap() error { return e.Err }
```

## 6.6 Multiple errors

```go
errs := errors.Join(err1, err2, nil)   // Go 1.20+; nils skipped, nil if all nil
errors.Is(errs, err1)                  // true
```

```go
var errs []error
for _, f := range files {
    if err := process(f); err != nil {
        errs = append(errs, fmt.Errorf("%s: %w", f, err))
    }
}
return errors.Join(errs...)
```

## 6.7 Choosing between the three

| Need | Use |
| --- | --- |
| Just report | `fmt.Errorf("...: %w", err)` |
| Caller branches on a known condition | sentinel + `errors.Is` |
| Caller needs fields (field name, status code, retryable) | custom type + `errors.As` |

## 6.8 panic and recover

`panic` is for **programmer bugs and unrecoverable states**, not control flow. It
unwinds the stack, running `defer`s, then crashes the process with a stack trace.

```go
panic("unreachable state")
```

Runtime panics you'll meet: nil pointer dereference, index out of range, write to nil
map, closing a closed channel, invalid type assertion, integer divide by zero.

`recover` only works **inside a deferred function**:

```go
func safe() (err error) {
    defer func() {
        if r := recover(); r != nil {
            err = fmt.Errorf("recovered: %v", r)
        }
    }()
    mightPanic()
    return nil
}
```

When to actually recover:
- At an HTTP handler / goroutine boundary, so one bad request doesn't kill the server.
- At a library's public boundary, converting internal panics to errors (parsers do this).
- Never as a general try/catch.

⚠️ A panic in a goroutine that nobody recovers **crashes the whole program** — a
`recover` in `main` will not save you.

```go
go func() {
    defer func() { if r := recover(); r != nil { log.Println("goroutine panic:", r) } }()
    work()
}()
```

`log.Fatal` (log + `os.Exit(1)`) is the blunt alternative — fine in `main`, never in a
library, and note it **skips all defers**.

## 6.9 Idioms & anti-patterns

```go
// ✅ early return, happy path stays un-indented
if err != nil { return err }
doWork()

// ❌ don't nest the success case
if err == nil { doWork() } else { return err }

// ✅ explicit ignore, with a reason
_ = f.Close()   // read-only file, close error irrelevant

// ❌ silent swallow
v, _ := strconv.Atoi(s)

// ✅ deferred close that CAN fail (writes!) — capture the error
func write(name string) (err error) {
    f, err := os.Create(name)
    if err != nil { return err }
    defer func() {
        if cerr := f.Close(); cerr != nil && err == nil { err = cerr }
    }()
    ...
}

// ✅ must-style helper, only for package init / tests
func must[T any](v T, err error) T {
    if err != nil { panic(err) }
    return v
}
```

| C# / TS / Python | Go |
| --- | --- |
| `throw new FooException()` | `return nil, ErrFoo` |
| `try/catch` | `if err != nil` |
| `catch (FooException e)` | `errors.Is` / `errors.As` |
| `finally` | `defer` |
| exception chaining / `innerException` | `%w` wrapping + `Unwrap()` |
| `catch` at the top of the stack | check at **every** level, add context, return |
