# nosqlite — build, test, run the examples.
#
# `make` on its own prints the available targets.
#
# A note if you are new to Make: the indentation in a recipe must be a TAB, not
# spaces. Everything below follows that.

# The shared library name differs per platform; pick it here so every target
# agrees. `uname -s` is Linux / Darwin / MINGW…
UNAME := $(shell uname -s)
ifeq ($(UNAME),Darwin)
	LIBNAME := libnosqlite.dylib
else ifeq ($(OS),Windows_NT)
	LIBNAME := nosqlite.dll
else
	LIBNAME := libnosqlite.so
endif

LIBPATH := python/nosqlite/$(LIBNAME)
# Both bindings load the same library; each keeps a copy next to its own
# package so the package is self-contained. `build` compiles once and copies.
TSLIBPATH := typescript/nosqlite/$(LIBNAME)

.PHONY: help build test test-race vet fmt cli example example-py example-ts ts-deps ts-check \
	conformance conformance-ts conformance-ts-deps conformance-py py-deps clean all

help:
	@echo "nosqlite targets:"
	@echo "  make build           build the C shared library into $(LIBPATH)"
	@echo "  make test            run the Go test suite"
	@echo "  make test-race       run the tests under the race detector"
	@echo "  make vet             run go vet"
	@echo "  make fmt             gofmt every Go file in place"
	@echo "  make cli             build the nsq inspection CLI into ./bin/nsq"
	@echo "  make example         run the Go example"
	@echo "  make example-py      build the library, then run the Python example"
	@echo "  make example-ts      build the library, then run the TypeScript example"
	@echo "  make ts-check        type-check the TypeScript binding with tsc"
	@echo "  make conformance     run the Go conformance suite (docs/testing.md)"
	@echo "  make conformance-ts  build the library, then run the TypeScript conformance suite"
	@echo "  make conformance-py  build the library, then run the Python conformance suite"
	@echo "  make all             fmt, vet, test, build, cli, all three conformance suites"
	@echo "  make clean           remove build artifacts and stray .nsq files"

# The shared library must exist before the Python package works at all.
# -buildmode=c-shared also emits a .h header next to the library.
build:
	go build -buildmode=c-shared -o $(LIBPATH) ./capi
	cp $(LIBPATH) $(TSLIBPATH)
	@echo "built $(LIBPATH) (copied to $(TSLIBPATH))"

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

cli:
	go build -o bin/nsq ./cmd/nsq
	@echo "built bin/nsq — try: ./bin/nsq stat <file>"

# The argument is a directory, not a file: each example is its own package main
# under examples/, so `go run ./examples/<name>` runs it.
example:
	go run ./examples/basic

# PYTHONPATH points at python/ so `import nosqlite` finds the package straight
# out of the checkout, with nothing installed.
example-py: build
	PYTHONPATH=python python3 examples/basic/basic.py

# koffi is the FFI the TypeScript binding uses; Node has none built in. It
# ships prebuilt binaries, so this is a download, not a compile. The
# `test -d` guard keeps repeat runs offline and instant.
ts-deps:
	@test -d typescript/node_modules || npm --prefix typescript install --no-audit --no-fund

# No compile step: Node >= 22.18 strips the types and runs the .ts file
# directly. The example imports the binding by relative path, so nothing has to
# be installed or linked beyond koffi above.
example-ts: build ts-deps
	node examples/basic/basic.ts

# Node strips the type annotations without ever checking them, so this is the
# only step that actually reads them. tsc emits nothing (see tsconfig.json).
ts-check: ts-deps
	typescript/node_modules/.bin/tsc -p typescript
	@echo "typescript: no type errors"

# The `conformance` build tag keeps this out of `make test` / `go test ./...`
# — see docs/testing.md for why it's a separate suite.
conformance:
	go test -tags=conformance ./conformance/...

# A separate node_modules from typescript/'s: this one needs jest and ts-jest,
# which the shipped binding has no reason to depend on.
conformance-ts-deps:
	@test -d conformance/typescript/node_modules || npm --prefix conformance/typescript install --no-audit --no-fund

# `ts-deps` as well as `conformance-ts-deps`: the suite imports the shipped
# binding, and that binding imports koffi out of typescript/node_modules. On a
# fresh clone, without it, jest fails with "Cannot find module 'koffi'".
conformance-ts: build ts-deps conformance-ts-deps
	npm --prefix conformance/typescript test

# One venv for all of the project's Python dev tooling, gitignored, lazily
# created. It lives at the root rather than beside the suite that currently uses
# it: the binding itself has no dependencies, so pytest is the only thing that
# ever goes in here, and an editor discovers ./.venv without being configured.
py-deps:
	@test -d .venv || { python3 -m venv .venv && echo "created .venv"; }
	@.venv/bin/pip install -q -r requirements-dev.txt

conformance-py: build py-deps
	PYTHONPATH=python .venv/bin/pytest conformance/python

all: fmt vet test build cli conformance conformance-ts conformance-py

clean:
	rm -f $(LIBPATH) $(TSLIBPATH) python/nosqlite/libnosqlite.h
	rm -rf bin typescript/node_modules conformance/typescript/node_modules .venv
	rm -f db/*.nsq db/*.nsq.trace
