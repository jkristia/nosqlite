// go.mod declares this directory to be a Go "module" — the unit Go uses for
// dependency management, roughly equivalent to a package.json or pyproject.toml.
//
// The module path below is also the import path: other code would write
//     import "github.com/jkristia/nosqlite"
// to use this package. It does not have to exist on GitHub for local builds to
// work; change it freely if you publish this somewhere else.
module github.com/jkristia/nosqlite

// The minimum Go toolchain version required to build this module.
go 1.22

// There is no "require" block: the design mandates standard library only, so
// this module has zero third-party dependencies.
