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

.PHONY: help build test test-race vet fmt cli example example-py clean all

help:
	@echo "nosqlite targets:"
	@echo "  make build       build the C shared library into $(LIBPATH)"
	@echo "  make test        run the Go test suite"
	@echo "  make test-race   run the tests under the race detector"
	@echo "  make vet         run go vet"
	@echo "  make fmt         gofmt every Go file in place"
	@echo "  make cli         build the nsq inspection CLI into ./bin/nsq"
	@echo "  make example     run the Go example"
	@echo "  make example-py  build the library, then run the Python example"
	@echo "  make all         fmt, vet, test, build, cli"
	@echo "  make clean       remove build artifacts and stray .nsq files"

# The shared library must exist before the Python package works at all.
# -buildmode=c-shared also emits a .h header next to the library.
build:
	go build -buildmode=c-shared -o $(LIBPATH) ./capi
	@echo "built $(LIBPATH)"

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

all: fmt vet test build cli

clean:
	rm -f $(LIBPATH) python/nosqlite/libnosqlite.h
	rm -rf bin
	rm -f db/*.nsq db/*.nsq.trace
