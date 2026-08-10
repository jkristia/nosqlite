package nosqlite

// catalog.go maps collection names to the small integer ids that appear in
// every record header.
//
// Why ids at all? Because every record in the single database file has to say
// which collection it belongs to, and spending 2 bytes on an integer beats
// repeating the string "users" a million times. The name appears exactly once
// in the file, in a "define collection" record.

import (
	"encoding/json"
	"fmt"
)

// maxCollections is the cap implied by the 2-byte coll field. Widening it would
// be a format change, hence a formatVersion bump.
const maxCollections = 65535

// validateCollectionName enforces [A-Za-z0-9_-]{1,64}.
//
// The restriction is deliberate: names end up in file records, trace lines and
// CLI arguments, and keeping them boring means none of those need escaping.
func validateCollectionName(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return fmt.Errorf("nosqlite: collection name %q must be 1-64 characters", name)
	}
	// Iterating a string with `range` yields runes (Unicode code points). Here
	// we only accept ASCII, so comparing runes to ASCII literals is fine.
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return fmt.Errorf("nosqlite: collection name %q may only contain A-Z a-z 0-9 _ -", name)
		}
	}
	return nil
}

// defineCollection assigns the next id, appends the catalog record, and
// registers the collection in memory.
//
// Caller must hold db.mu for writing, and must have checked that the name is
// valid and not already present.
func (db *DB) defineCollection(name string) (*Collection, error) {
	if int(db.nextID) > maxCollections {
		return nil, fmt.Errorf("nosqlite: cannot create %q: the %d collection limit is reached",
			name, maxCollections)
	}
	id := db.nextID

	payload, err := json.Marshal(collectionDefinition{ID: id, Name: name})
	if err != nil {
		return nil, fmt.Errorf("nosqlite: encoding collection record: %w", err)
	}
	if _, _, err := db.appendRecord(opDefineCollection, id, payload); err != nil {
		return nil, err
	}
	if err := db.syncIfNeeded(); err != nil {
		return nil, err
	}

	c := db.registerCollection(id, name)
	db.traceDefine(name, id)
	return c, nil
}

// applyDefineCollection replays a catalog record found during Open.
func (db *DB) applyDefineCollection(payload []byte) error {
	id, name, err := DecodeCollectionRecord(payload)
	if err != nil {
		return err
	}
	if id == 0 {
		return fmt.Errorf("collection id 0 is not valid (ids start at 1)")
	}
	if err := validateCollectionName(name); err != nil {
		return err
	}
	if existing, ok := db.byID[id]; ok {
		return fmt.Errorf("collection id %d defined twice (%q and %q)", id, existing.name, name)
	}
	if _, ok := db.catalog[name]; ok {
		return fmt.Errorf("collection %q defined twice", name)
	}
	db.registerCollection(id, name)
	return nil
}

// registerCollection adds a collection to both lookup maps and moves nextID
// past it. Caller must hold db.mu for writing.
func (db *DB) registerCollection(id uint16, name string) *Collection {
	c := &Collection{db: db, name: name, id: id}
	db.catalog[name] = c
	db.byID[id] = c
	if id >= db.nextID {
		db.nextID = id + 1
	}
	return c
}
