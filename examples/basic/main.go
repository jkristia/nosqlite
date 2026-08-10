// Command basic is a tour of the Go API.
//
//	go run ./examples/basic
//
// Note that the argument is the *directory*, not a file: a Go program is a
// directory of files declaring `package main`, so each example gets its own
// folder and they never collide.
//
// It writes ./db/example.nsq (and ./db/example.nsq.trace) and leaves both
// behind when it exits, so you can inspect them afterwards:
//
//	cat db/example.nsq.trace
//	./bin/nsq stat db/example.nsq
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/jkristia/nosqlite"
)

func main() {
	const dir = "./db"
	const path = dir + "/example.nsq"

	// MkdirAll creates the directory and any missing parents, and — unlike
	// Mkdir — returns nil rather than an error when it already exists, so it is
	// safe to call on every run. 0o755 is the usual permission for a directory:
	// the owner may write, everyone may read and enter it.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}

	// Start from scratch each run, so the numbers printed below are
	// predictable — otherwise every run would append another five people to the
	// same collection. Nothing is deleted at the end: both files stay on disk.
	os.Remove(path)
	os.Remove(path + ".trace")

	// --- opening ----------------------------------------------------------

	// Options are optional; this one turns on the trace file so you can see
	// what the database did afterwards.
	db, err := nosqlite.Open(path, nosqlite.WithTrace(nosqlite.TraceAll))
	if err != nil {
		log.Fatal(err)
	}
	// `defer` runs when main returns, which is the idiomatic way to be sure the
	// database gets closed however the function exits.
	defer db.Close()

	users, err := db.Collection("users")
	if err != nil {
		log.Fatal(err)
	}

	// --- inserting --------------------------------------------------------

	id, err := users.Insert(map[string]any{
		"name": "Ada",
		"age":  36,
		"tags": []any{"math", "code"},
		"address": map[string]any{
			"city": "London",
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("inserted Ada with _id:", id)

	// InsertMany is one write and one disk flush for the whole batch.
	ids, err := users.InsertMany([]map[string]any{
		{"name": "Grace", "age": 45, "address": map[string]any{"city": "New York"}},
		{"name": "Alan", "age": 41, "tags": []any{"math"}},
		{"name": "Edsger", "age": 41, "address": map[string]any{"city": "Austin"}},
		{"name": "Barbara", "age": 63, "tags": []any{"biology", "code"}},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("inserted %d more documents\n", len(ids))
	fmt.Println("collection now holds:", users.Len(), "documents")

	// --- querying ---------------------------------------------------------

	fmt.Println("\n-- everyone 40 or older, oldest first --")
	docs, err := users.Find(nosqlite.Query{
		Filter: map[string]any{"age": map[string]any{"$gte": 40}},
		Sort:   []nosqlite.SortKey{{Field: "age", Desc: true}},
		Limit:  10,
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, doc := range docs {
		// Remember: JSON has no integer type, so numbers come back as float64.
		fmt.Printf("  %-8s %.0f\n", doc["name"], doc["age"])
	}

	fmt.Println("\n-- anyone tagged 'math' (arrays match element-wise) --")
	docs, err = users.Find(nosqlite.Query{Filter: map[string]any{"tags": "math"}})
	if err != nil {
		log.Fatal(err)
	}
	for _, doc := range docs {
		fmt.Printf("  %s\n", doc["name"])
	}

	fmt.Println("\n-- dotted paths reach into nested objects --")
	doc, err := users.FindOne(map[string]any{"address.city": "Austin"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  %v lives in Austin\n", doc["name"])

	fmt.Println("\n-- $or, and counting --")
	n, err := users.Count(map[string]any{"$or": []any{
		map[string]any{"age": map[string]any{"$lt": 40}},
		map[string]any{"age": map[string]any{"$gt": 60}},
	}})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  %d people are under 40 or over 60\n", n)

	// --- streaming --------------------------------------------------------

	// ForEach keeps memory flat no matter how many documents match: nothing is
	// retained between callbacks. This is the one to reach for over a big
	// result set.
	fmt.Println("\n-- streaming with ForEach --")
	total := 0.0
	err = users.ForEach(nosqlite.Query{}, func(doc map[string]any) error {
		if age, ok := doc["age"].(float64); ok {
			total += age
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  average age: %.1f\n", total/float64(users.Len()))

	fmt.Println("\ncollections in this database:", db.Collections())
	fmt.Println("\nDatabase file:", path)
	fmt.Println("Trace of everything above is in", path+".trace")
	fmt.Println("Both files are left in place — try: cat " + path + ".trace")
	fmt.Println("Try: NOSQLITE_TRACE=verbose go run ./examples/basic")
}
