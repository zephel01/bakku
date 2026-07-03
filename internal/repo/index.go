package repo

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/zephel01/bakku/internal/backend"
	"github.com/zephel01/bakku/internal/crypto"
)

// IndexLocation records where a blob lives: which pack, and the byte range +
// type within it.
type IndexLocation struct {
	PackID          string   `json:"pack"`
	Offset          int64    `json:"off"`
	Length          int64    `json:"len"`
	Type            BlobType `json:"type"`
	UncompressedLen int64    `json:"ulen"`
}

// Index maps blob ids (hex) to their locations. It is the in-memory dedup and
// lookup structure, persisted (encrypted) under index/ in the backend. Safe for
// concurrent use.
type Index struct {
	mu  sync.RWMutex
	m   map[string]IndexLocation
	key []byte // index encryption subkey
}

func newIndex(key []byte) *Index {
	return &Index{m: make(map[string]IndexLocation), key: key}
}

// Has reports whether blob id is already known (used for dedup).
func (ix *Index) Has(id string) bool {
	ix.mu.RLock()
	_, ok := ix.m[id]
	ix.mu.RUnlock()
	return ok
}

// Lookup returns the location for id.
func (ix *Index) Lookup(id string) (IndexLocation, bool) {
	ix.mu.RLock()
	loc, ok := ix.m[id]
	ix.mu.RUnlock()
	return loc, ok
}

// Add records a blob location. Idempotent for repeated ids.
func (ix *Index) Add(id string, loc IndexLocation) {
	ix.mu.Lock()
	if _, ok := ix.m[id]; !ok {
		ix.m[id] = loc
	}
	ix.mu.Unlock()
}

// Len returns the number of indexed blobs.
func (ix *Index) Len() int {
	ix.mu.RLock()
	n := len(ix.m)
	ix.mu.RUnlock()
	return n
}

// indexFile is the on-disk JSON structure (plaintext before encryption).
type indexFile struct {
	Version int                      `json:"version"`
	Blobs   map[string]IndexLocation `json:"blobs"`
}

// Save serializes the index, encrypts it, and writes it to index/<id> in the
// backend. Returns the index id.
func (ix *Index) Save(ctx context.Context, be backend.Backend) (string, error) {
	ix.mu.RLock()
	f := indexFile{Version: repoVersion, Blobs: make(map[string]IndexLocation, len(ix.m))}
	for k, v := range ix.m {
		f.Blobs[k] = v
	}
	ix.mu.RUnlock()

	plain, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	sealed, err := crypto.Seal(ix.key, plain, nil)
	if err != nil {
		return "", err
	}
	id, err := randomHex(16)
	if err != nil {
		return "", err
	}
	key := "index/" + id
	if err := be.Save(ctx, key, bytesReader(sealed), int64(len(sealed))); err != nil {
		return "", err
	}
	return id, nil
}

// loadIndex reads and merges all index/ files from the backend into a single
// in-memory Index.
func loadIndex(ctx context.Context, be backend.Backend, key []byte) (*Index, error) {
	ix := newIndex(key)
	err := be.List(ctx, "index", func(k string, _ int64) error {
		blob, err := readAll(ctx, be, k)
		if err != nil {
			return err
		}
		plain, err := crypto.Open(key, blob, nil)
		if err != nil {
			return err
		}
		var f indexFile
		if err := json.Unmarshal(plain, &f); err != nil {
			return err
		}
		ix.mu.Lock()
		for id, loc := range f.Blobs {
			if _, ok := ix.m[id]; !ok {
				ix.m[id] = loc
			}
		}
		ix.mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ix, nil
}
