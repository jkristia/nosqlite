# 12. cgo — calling C, and being called from C

Two directions, and they are very different jobs:

1. **Go calls C** — link an existing C library into a Go program.
2. **Go is called by others** — build a **shared library** with a C ABI, so
   Python (`ctypes`/`cffi`), Node (`ffi-napi`, Bun FFI), C#(`DllImport`), etc. can call it.

Direction 2 is how a Go core gets language bindings.

---

## 12.1 Go calls C

```go
package main

/*
#include <stdlib.h>
#include <string.h>

static int add(int a, int b) { return a + b; }
*/
import "C"        // ← must be its OWN import, directly after the comment
import "fmt"

func main() {
    fmt.Println(int(C.add(2, 3)))
}
```

- The comment immediately above `import "C"` is real C, compiled by your system C
  compiler. **No blank line between them.**
- `C` is a pseudo-package: `C.int`, `C.char`, `C.add`, `C.malloc`, `C.free`.
- Directives for the C toolchain:

```go
/*
#cgo CFLAGS: -I${SRCDIR}/include
#cgo LDFLAGS: -L${SRCDIR}/lib -lmylib
#cgo darwin LDFLAGS: -framework CoreFoundation
#include "mylib.h"
*/
import "C"
```

## 12.2 Type conversions

| Go | C |
| --- | --- |
| `C.int`, `C.long`, `C.double` | `int`, `long`, `double` |
| `C.char` | `char` |
| `*C.char` | `char *` (NUL-terminated string) |
| `unsafe.Pointer` | `void *` |
| `C.size_t` | `size_t` |

```go
cs := C.CString(goStr)        // Go string → malloc'd char* (a COPY)
defer C.free(unsafe.Pointer(cs))

s := C.GoString(cp)                    // char* → Go string (copies)
s := C.GoStringN(cp, C.int(n))         // with explicit length
b := C.GoBytes(unsafe.Pointer(p), C.int(n))   // void*+len → []byte
```

**`C.CString` allocates with `malloc` and the Go GC will never free it.** Every
`CString` needs a matching `C.free` or you leak.

Numeric conversions are always explicit: `C.int(goInt)`, `int(cInt)`.

## 12.3 The pointer rules (the part that actually bites)

- You may pass a Go pointer to C **for the duration of the call only**. C must not
  store it and use it later — the Go GC can move or collect the object.
- You may **never** store a Go pointer in C memory. The runtime detects many violations
  and panics: `cgo argument has Go pointer to Go pointer`.
- Consequence: **you cannot hand a `*MyStruct` out to another language.**

The standard workaround is a **handle registry**: keep the Go object in a map, hand out
an opaque integer.

```go
var (
    mu      sync.Mutex
    nextID  int64
    objects = map[int64]*Thing{}
)

func register(t *Thing) int64 {
    mu.Lock(); defer mu.Unlock()
    nextID++
    objects[nextID] = t
    return nextID
}

func lookup(id int64) (*Thing, bool) {
    mu.Lock(); defer mu.Unlock()
    t, ok := objects[id]
    return t, ok
}
```

The caller holds only an `int64`; every C-facing function takes that id, looks the
object up, and works with it. (`runtime/cgo.Handle` in the stdlib does the same thing
for the single-value case.)

## 12.4 Go called from C — exporting a shared library

```go
package main

/*
#include <stdlib.h>
*/
import "C"

//export nsq_open
func nsq_open(path *C.char) C.longlong {
    p := C.GoString(path)
    t, err := Open(p)
    if err != nil { return -1 }
    return C.longlong(register(t))
}

//export nsq_free
func nsq_free(p *C.char) { C.free(unsafe.Pointer(p)) }

func main() {}     // required, and must be empty, for c-shared
```

Rules for `//export`:
- The package must be `package main`, with an **empty `main()`**.
- `//export Name` must sit directly above the function, no blank line, and the name
  must match exactly.
- Only **free functions** can be exported — **not methods**. A method has to be wrapped
  in a plain function by hand.
- Parameters and results must be C-compatible types. No Go strings, slices, maps,
  interfaces or pointers-to-Go-memory across the boundary.
- Exported Go identifiers are *not* automatically visible: an exported Go method with
  no `//export` wrapper is unreachable from the other side.

Build modes:

```bash
go build -buildmode=c-shared  -o libthing.so  ./capi   # .so / .dylib / .dll + header
go build -buildmode=c-archive -o libthing.a   ./capi   # static
```

Go also generates `libthing.h` with the C prototypes — hand that to the C/FFI consumer.

## 12.5 Returning data across the boundary

You cannot return a Go slice or string. The three workable shapes:

1. **Caller-allocated buffer** (best): caller passes `(char *buf, int len)`, you fill it
   and return the number of bytes written (or the required size if too small).

```go
//export nsq_get
func nsq_get(id C.longlong, buf *C.char, n C.int) C.int {
    data := lookupData(id)
    if C.int(len(data)) > n { return C.int(-len(data)) }   // signal required size
    C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&data[0]), C.size_t(len(data)))
    return C.int(len(data))
}
```

2. **Callee-allocated with an explicit free**: return `C.CString(...)` and export a
   `nsq_free` the caller must call. Simple, but the ownership contract is easy to break.

3. **Serialize everything** — return one JSON string per call. Slowest, but by far the
   simplest boundary, and usually fast enough.

Errors: there is no exception mechanism across FFI. Conventions are a negative return
code, or an out-parameter `char **err`.

## 12.6 Costs and consequences

- A cgo call is roughly **tens of nanoseconds** vs ~1 ns for a Go call — fine per
  operation, expensive in a tight per-element loop. Batch at the boundary.
- cgo goroutine calls may need an OS thread; heavy cgo undermines the scheduler.
- **`CGO_ENABLED=1` breaks easy cross-compilation** — you now need a target C toolchain.
- Build times go up; `go vet`, race detector and profiling get less useful inside C.
- Any C crash takes down the whole process; no panic, no stack trace.

```bash
CGO_ENABLED=0 go build ./...    # force pure Go — fully static, cross-compiles anywhere
CGO_ENABLED=1 go build ./capi   # needed for c-shared
```

Rule of thumb: **keep the cgo layer as thin as possible.** A handful of `nsq_*`
functions that translate types and delegate immediately to normal Go code, with all
real logic in pure-Go packages that stay testable with `go test`.

## 12.7 Consuming the shared library

```python
# Python — ctypes
import ctypes
lib = ctypes.CDLL("./libthing.dylib")
lib.nsq_open.argtypes = [ctypes.c_char_p]
lib.nsq_open.restype  = ctypes.c_longlong
h = lib.nsq_open(b"/tmp/db")
```

```ts
// Bun FFI
import { dlopen, FFIType } from "bun:ffi";
const { symbols } = dlopen("./libthing.dylib", {
  nsq_open: { args: [FFIType.cstring], returns: FFIType.i64 },
});
```

```csharp
[DllImport("thing")] static extern long nsq_open(string path);
```

All three see the same flat C API: opaque integer handles, primitives, and byte buffers.
