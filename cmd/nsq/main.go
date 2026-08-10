// Command nsq inspects nosqlite database files.
//
// The trace file says *what the API did*; nsq says *what is actually in the
// file*, in the same vocabulary. The off= field in a trace line is directly
// usable as `nsq dump --from <off>`, which is the whole debugging loop for a
// storage engine.
//
//	nsq stat   demo.nsq
//	nsq dump   demo.nsq [--coll users] [--from 309200144] [--limit 5]
//	nsq verify demo.nsq
//	nsq find   demo.nsq users '{"age":{"$gte":30}}' [--sort age:desc] [--limit 10]
//
// This is a `package main`, which is what makes it an executable rather than a
// library. `go build ./cmd/nsq` produces the binary.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/jkristia/nosqlite"
)

func main() {
	// os.Args[0] is the program name, so the subcommand is os.Args[1].
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "stat":
		err = cmdStat(os.Args[2:])
	case "dump":
		err = cmdDump(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "find":
		err = cmdFind(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "nsq: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "nsq: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `nsq — inspect a nosqlite database file

  nsq stat   <file>                       header, collections, record counts, size
  nsq dump   <file> [flags]               every record: offset, len, op, collection, payload
  nsq verify <file>                       walk every checksum, report bad records
  nsq find   <file> <collection> [filter] run a query against the database

dump flags:
  --coll <name>    only records of this collection
  --from <offset>  start at this byte offset (as printed by the trace file's off=)
  --limit <n>      stop after n records
  --payload=false  omit payloads, print only the framing

find flags:
  --sort <field:asc|desc>[,...]
  --skip <n>
  --limit <n>      default 10; 0 means no limit
`)
}

// ---------------------------------------------------------------------------
// stat
// ---------------------------------------------------------------------------

// splitPositional peels the leading positional arguments off an argument list
// and returns them along with whatever is left for the flag parser.
//
// Go's flag package stops parsing at the first argument that does not start
// with "-", so `nsq dump file.nsq --limit 3` would otherwise silently ignore
// --limit. Taking the positionals off the front first is the simplest fix, and
// it keeps the natural `nsq <cmd> <file> [flags]` shape.
//
// `want` is the maximum number of positionals to take; fewer is fine, and an
// argument starting with "-" ends the run.
func splitPositional(args []string, want int) (positional, flags []string) {
	i := 0
	for i < len(args) && i < want && !strings.HasPrefix(args[i], "-") {
		i++
	}
	return args[:i], args[i:]
}

func cmdStat(args []string) error {
	pos, flags := splitPositional(args, 1)
	fs := flag.NewFlagSet("stat", flag.ExitOnError)
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("usage: nsq stat <file>")
	}
	path := pos[0]

	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	// Collect per-collection counts and byte totals in one pass.
	names := map[uint16]string{}
	counts := map[uint16]int{}
	bytes := map[uint16]int64{}
	var records, bad int

	header, err := nosqlite.WalkFile(path, func(r nosqlite.RawRecord) error {
		records++
		if !r.CRCOK {
			bad++
		}
		switch r.Op {
		case 4: // define collection
			id, name, err := nosqlite.DecodeCollectionRecord(r.Payload)
			if err == nil {
				names[id] = name
				if _, seen := counts[id]; !seen {
					counts[id] = 0
				}
			}
		case 1: // insert
			counts[r.Coll]++
			bytes[r.Coll] += r.Total()
		}
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Printf("file        %s\n", path)
	fmt.Printf("size        %d bytes\n", info.Size())
	fmt.Printf("format      %d\n", header.Format)
	fmt.Printf("created     %s\n", header.Created.UTC().Format("2006-01-02T15:04:05Z"))
	fmt.Printf("records     %d\n", records)
	if bad > 0 {
		fmt.Printf("bad crc     %d  (run `nsq verify %s`)\n", bad, path)
	}
	fmt.Printf("collections %d\n", len(names))

	ids := make([]int, 0, len(names))
	for id := range names {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)

	fmt.Printf("\n%-4s %-24s %10s %14s %10s\n", "id", "name", "documents", "bytes", "avg")
	for _, id := range ids {
		u := uint16(id)
		n := counts[u]
		avg := int64(0)
		if n > 0 {
			avg = bytes[u] / int64(n)
		}
		fmt.Printf("%-4d %-24s %10d %14d %10d\n", id, names[u], n, bytes[u], avg)
	}
	return nil
}

// ---------------------------------------------------------------------------
// dump
// ---------------------------------------------------------------------------

func cmdDump(args []string) error {
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	coll := fs.String("coll", "", "only records of this collection")
	from := fs.Int64("from", 0, "start at this byte offset")
	limit := fs.Int("limit", 0, "stop after n records (0 = all)")
	showPayload := fs.Bool("payload", true, "print record payloads")
	pos, flags := splitPositional(args, 1)
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("usage: nsq dump <file> [flags]")
	}
	path := pos[0]

	names := map[uint16]string{}
	shown := 0

	// errStop lets the callback end the walk once --limit is reached.
	errStop := fmt.Errorf("stop")

	_, err := nosqlite.WalkFile(path, func(r nosqlite.RawRecord) error {
		if r.Op == 4 {
			if id, name, err := nosqlite.DecodeCollectionRecord(r.Payload); err == nil {
				names[id] = name
			}
		}
		name := names[r.Coll]
		if name == "" {
			name = "?" + strconv.Itoa(int(r.Coll))
		}
		if *coll != "" && name != *coll {
			return nil
		}
		if r.Offset < *from {
			return nil
		}

		crc := "ok"
		if !r.CRCOK {
			crc = "BAD"
		}
		fmt.Printf("%12d  len=%-8d %-8s %-16s crc=%-4s", r.Offset, r.Length, opLabel(r.Op), name, crc)
		if *showPayload {
			fmt.Printf("  %s", string(r.Payload))
		}
		fmt.Println()

		shown++
		if *limit > 0 && shown >= *limit {
			return errStop
		}
		return nil
	})
	if err != nil && err != errStop {
		return err
	}
	return nil
}

func opLabel(op uint8) string {
	switch op {
	case 1:
		return "insert"
	case 2:
		return "delete"
	case 3:
		return "replace"
	case 4:
		return "define"
	case 5:
		return "begin"
	case 6:
		return "commit"
	default:
		return "op" + strconv.Itoa(int(op))
	}
}

// ---------------------------------------------------------------------------
// verify
// ---------------------------------------------------------------------------

func cmdVerify(args []string) error {
	pos, flags := splitPositional(args, 1)
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("usage: nsq verify <file>")
	}
	path := pos[0]

	var records, bad int
	var firstBad int64 = -1

	_, err := nosqlite.WalkFile(path, func(r nosqlite.RawRecord) error {
		records++
		if !r.CRCOK {
			bad++
			if firstBad < 0 {
				firstBad = r.Offset
			}
			fmt.Printf("BAD CHECKSUM at offset %d (op=%s coll=%d len=%d)\n",
				r.Offset, opLabel(r.Op), r.Coll, r.Length)
		}
		return nil
	})
	if err != nil {
		// A torn tail shows up here as an error; that is information, not a
		// crash, so report it and carry on to the summary.
		fmt.Printf("%v\n", err)
	}

	fmt.Printf("checked %d records, %d bad\n", records, bad)
	if bad > 0 {
		// A non-zero exit status is what makes this usable from a script.
		os.Exit(1)
	}
	return nil
}

// ---------------------------------------------------------------------------
// find
// ---------------------------------------------------------------------------

func cmdFind(args []string) error {
	fs := flag.NewFlagSet("find", flag.ExitOnError)
	sortSpec := fs.String("sort", "", "field:asc|desc, comma separated")
	skip := fs.Int("skip", 0, "documents to skip")
	limit := fs.Int("limit", 10, "documents to return (0 = no limit)")
	pos, flags := splitPositional(args, 3)
	if err := fs.Parse(flags); err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: nsq find <file> <collection> [filter] [flags]")
	}
	path, coll := pos[0], pos[1]
	filterJSON := ""
	if len(pos) > 2 {
		filterJSON = pos[2]
	}

	var filter map[string]any
	if filterJSON != "" {
		if err := json.Unmarshal([]byte(filterJSON), &filter); err != nil {
			return fmt.Errorf("parsing filter: %w", err)
		}
	}
	keys, err := parseSort(*sortSpec)
	if err != nil {
		return err
	}

	// Opening read/write is not ideal for an inspection tool, but replay is
	// also the only thing that builds the index a query needs.
	db, err := nosqlite.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()

	c, err := db.Collection(coll)
	if err != nil {
		return err
	}

	// ForEach rather than Find, so a huge result set does not have to fit in
	// memory before the first line is printed.
	enc := json.NewEncoder(os.Stdout)
	return c.ForEach(nosqlite.Query{Filter: filter, Sort: keys, Skip: *skip, Limit: *limit},
		func(doc map[string]any) error {
			return enc.Encode(doc)
		})
}

// parseSort turns "age:desc,name" into []SortKey.
func parseSort(spec string) ([]nosqlite.SortKey, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	var keys []nosqlite.SortKey
	for _, part := range strings.Split(spec, ",") {
		field, dir, found := strings.Cut(strings.TrimSpace(part), ":")
		if field == "" {
			return nil, fmt.Errorf("bad sort spec %q", spec)
		}
		desc := false
		if found {
			switch strings.ToLower(dir) {
			case "asc", "1", "":
			case "desc", "-1":
				desc = true
			default:
				return nil, fmt.Errorf("bad sort direction %q (want asc or desc)", dir)
			}
		}
		keys = append(keys, nosqlite.SortKey{Field: field, Desc: desc})
	}
	return keys, nil
}
