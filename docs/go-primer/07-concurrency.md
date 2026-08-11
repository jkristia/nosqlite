# 7. Concurrency

No `async`/`await`, no promises, no colored functions. Every function is potentially
concurrent; you choose at the **call site** with `go`. Blocking calls are cheap because
goroutines are not OS threads.

> *"Don't communicate by sharing memory; share memory by communicating."* — and yet:
> a plain `sync.Mutex` is often the simpler, correct answer. Use channels for
> *handing off work*, mutexes for *protecting state*.

---

## 7.1 Goroutines

```go
go doWork()                     // returns immediately
go func() { work(x) }()         // inline; note the trailing ()
```

- ~2 KB initial stack, grown on demand. Hundreds of thousands are normal.
- Scheduled by the Go runtime onto `GOMAXPROCS` OS threads.
- **No handle, no id, no join, no cancel.** You cannot kill a goroutine from outside —
  you signal it (channel or `context`) and it returns on its own.
- When `main` returns, the process exits and all goroutines die instantly.
- A goroutine that never returns is a **leak**: its memory and anything it references
  is retained forever.

| TS / C# | Go |
| --- | --- |
| `async function` / `Task` | just a function |
| `await f()` | `f()` — blocking is fine |
| `f()` without await | `go f()` |
| `Promise.all` | `sync.WaitGroup` or `errgroup` |
| `CancellationToken` | `context.Context` |
| `Task<T>` result | a channel, or a pointer + WaitGroup |

## 7.2 Waiting: WaitGroup

```go
var wg sync.WaitGroup
for _, u := range urls {
    wg.Add(1)
    go func() {                  // Go 1.22+: u is per-iteration, safe to capture
        defer wg.Done()
        fetch(u)
    }()
}
wg.Wait()
```

⚠️ Before Go 1.22 the loop variable was shared and you needed `go func(u string){...}(u)`.
Check the `go` line in `go.mod` — it selects the semantics.

Collecting results without a mutex — each goroutine writes its own index:

```go
results := make([]Result, len(urls))
var wg sync.WaitGroup
for i, u := range urls {
    wg.Add(1)
    go func() { defer wg.Done(); results[i] = fetch(u) }()
}
wg.Wait()
```

`golang.org/x/sync/errgroup` adds error propagation, cancellation and a concurrency
limit — the closest thing to `Promise.all`.

## 7.3 Channels

Typed, blocking queues.

```go
ch := make(chan int)         // unbuffered: send blocks until a receiver is ready
ch := make(chan int, 10)     // buffered: send blocks only when full

ch <- 1                      // send
v := <-ch                    // receive
v, ok := <-ch                // ok == false → closed AND drained
close(ch)                    // sender's job, never the receiver's
for v := range ch { }        // loops until closed
```

Directional types document intent and are compiler-checked:

```go
func produce(out chan<- int)  { out <- 1 }   // send-only
func consume(in  <-chan int)  { <-in }       // receive-only
```

Rules:
- **Only the sender closes**, and only when no more sends will happen.
- Send on a closed channel → panic. Close twice → panic.
- Receive from a closed channel → immediate zero value (that's what `ok` is for).
- Send/receive on a **nil** channel blocks forever (occasionally useful in `select`).
- Unbuffered = synchronization point (a rendezvous). Buffered = decoupling.

## 7.4 select

```go
select {
case v := <-in:
    use(v)
case out <- x:
    ...
case <-time.After(time.Second):
    return errors.New("timeout")
case <-ctx.Done():
    return ctx.Err()
default:
    // runs if nothing else is ready — makes the whole select non-blocking
}
```

- Blocks until one case is ready; picks **randomly** among ready cases.
- A `select {}` with no cases blocks forever.

## 7.5 Patterns

**Worker pool** — bounded parallelism:

```go
jobs := make(chan Job)
results := make(chan Result)

for w := 0; w < 8; w++ {
    go func() { for j := range jobs { results <- process(j) } }()
}

go func() {
    for _, j := range allJobs { jobs <- j }
    close(jobs)                     // workers' range loops end
}()

for i := 0; i < len(allJobs); i++ { use(<-results) }
```

**Semaphore** — limit concurrency without a pool:

```go
sem := make(chan struct{}, 5)
for _, t := range tasks {
    sem <- struct{}{}
    go func() { defer func() { <-sem }(); do(t) }()
}
```

**Signal / done channel** — `struct{}` costs zero bytes:

```go
done := make(chan struct{})
go func() { defer close(done); work() }()
<-done                     // wait; close broadcasts to ALL waiters
```

**Timeout on any operation:**

```go
select {
case res := <-slowOp():
    return res, nil
case <-time.After(2 * time.Second):
    return nil, errors.New("timed out")
}
```

## 7.6 sync

```go
var mu sync.Mutex
mu.Lock()
defer mu.Unlock()
```

```go
type Counter struct {
    mu sync.Mutex          // put the lock ABOVE the fields it guards
    n  map[string]int
}
func (c *Counter) Inc(k string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.n[k]++
}
```

- ⚠️ Must be a **pointer receiver** — a value receiver copies the mutex and the lock
  protects nothing. `go vet` flags copied locks.
- `sync.RWMutex`: many concurrent `RLock`s, one exclusive `Lock`. Only worth it when
  reads dominate heavily.
- `sync.Once` — exactly-once init:

```go
var once sync.Once
once.Do(func() { conn = connect() })
```

- `sync/atomic` — lock-free counters and flags:

```go
var n atomic.Int64
n.Add(1)
n.Load()

var ready atomic.Bool
var cfg atomic.Pointer[Config]      // swap a whole config atomically
```

- `sync.Map` — only for two narrow cases (append-only caches, disjoint keys per
  goroutine). Otherwise a plain map + `RWMutex` is faster and clearer.
- `sync.Pool` — reuse allocations in hot paths. Measure before reaching for it.

## 7.7 context

The standard way to carry cancellation, deadlines and request-scoped values.

```go
func Fetch(ctx context.Context, url string) (*Resp, error) {
    req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
    ...
}
```

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()                                            // ALWAYS defer cancel

ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
ctx, cancel := context.WithDeadline(ctx, t)

<-ctx.Done()        // closed on cancel/timeout
ctx.Err()           // context.Canceled | context.DeadlineExceeded
```

Rules:
- **First parameter, always named `ctx`.** Never store one in a struct.
- `context.Background()` at the top of `main`; `context.TODO()` as a placeholder.
- Propagate it through every blocking call, and check it in long loops:

```go
for _, item := range items {
    select {
    case <-ctx.Done(): return ctx.Err()
    default:
    }
    process(item)
}
```

- `context.WithValue` is for request-scoped metadata (trace id), **not** for passing
  arguments. Use an unexported key type to avoid collisions.

## 7.8 The race detector

Any unsynchronized concurrent access where at least one side writes is a **data race** —
undefined behaviour, not just a wrong number.

```bash
go test -race ./...
go run -race ./cmd/app
```

- ~10x slower, ~5x memory. Run it in CI, not in production.
- It only detects races that **actually happen** during the run. A clean run is not a proof.

## 7.9 Checklist before shipping concurrent code

- [ ] Every goroutine has a guaranteed exit path (closed channel, ctx, or finite work).
- [ ] Every channel has exactly one closer, and it's the sender.
- [ ] Every `Lock()` has a `defer Unlock()`.
- [ ] Locks are on pointer receivers; the struct is never copied.
- [ ] Every `context.WithX` has a `defer cancel()`.
- [ ] Shared maps/slices are guarded, or provably not shared.
- [ ] `go test -race` is clean.
