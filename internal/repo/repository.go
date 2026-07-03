// Package repo implements bakku's content-addressable, encrypted repository
// format (restic-like): a config, key files, packs, indexes, and snapshots
// stored through a backend.Backend.
//
// Public API overview (for subsequent agents):
//
//	Init(ctx, be, password)      -> create a fresh repository
//	Open(ctx, be, password)      -> open an existing repository (loads index)
//	(*Repository).SaveBlob(...)  -> dedup+compress+encrypt+pack a plaintext blob
//	(*Repository).LoadBlob(id)   -> fetch+decrypt+decompress a blob
//	(*Repository).Flush(ctx)     -> flush the open pack + persist the index
//	(*Repository).SaveTree/LoadTree
//	(*Repository).SaveSnapshot / ListSnapshots / LoadSnapshot / FindSnapshot
//	(*Repository).ChunkerKey()   -> keyed-FastCDC key for the archiver
//
// SaveBlob is safe for concurrent use by multiple goroutines.
package repo

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/zephel01/bakku/internal/backend"
	"github.com/zephel01/bakku/internal/chunker"
	"github.com/zephel01/bakku/internal/crypto"
)

const repoVersion = 1

// BLAKE3 derive-key contexts. Each purpose gets an independent subkey from the
// master key so a compromise of one domain does not reveal the others.
const (
	ctxDataKey  = "github.com/zephel01/bakku 2026 pack data key"
	ctxIndexKey = "github.com/zephel01/bakku 2026 index key"
	ctxSnapKey  = "github.com/zephel01/bakku 2026 snapshot key"
)

// repoConfig is the plaintext repository config stored under "config".
type repoConfig struct {
	Version    int    `json:"version"`
	ID         string `json:"id"`          // hex repo id
	ChunkerMin int    `json:"chunker_min"` // recorded for verification/rebuild
	ChunkerAvg int    `json:"chunker_avg"`
	ChunkerMax int    `json:"chunker_max"`
	Algorithm  string `json:"algorithm"` // "kfastcdc"
}

// Repository is an open handle to a bakku repository.
type Repository struct {
	be     backend.Backend
	cfg    repoConfig
	master []byte

	dataKey  []byte
	indexKey []byte
	snapKey  []byte
	chunkKey []byte

	index *Index

	mu         sync.Mutex          // guards the open pack writer, pending, dirtyIx
	pack       *packWriter
	pending    []pendingBlob       // blobs in the open pack awaiting a pack id
	pendingSet map[string]struct{} // ids in the open pack, for pre-flush dedup
	dirtyIx    bool                // true if blobs were added since last index Save
}

// Init creates a new repository in be, protected by password. It fails if a
// config already exists.
func Init(ctx context.Context, be backend.Backend, password []byte) (*Repository, error) {
	if _, err := be.Stat(ctx, "config"); err == nil {
		return nil, errors.New("repo: repository already initialized")
	} else if !backend.IsNotExist(err) {
		return nil, err
	}

	master, err := crypto.NewMasterKey()
	if err != nil {
		return nil, err
	}
	repoID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	cfg := repoConfig{
		Version:    repoVersion,
		ID:         repoID,
		ChunkerMin: chunker.MinSize,
		ChunkerAvg: chunker.NormalSize,
		ChunkerMax: chunker.MaxSize,
		Algorithm:  "kfastcdc",
	}
	cfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := be.Save(ctx, "config", bytesReader(cfgBytes), int64(len(cfgBytes))); err != nil {
		return nil, err
	}

	// Write the key file.
	keyID, keyBlob, err := createKeyFile(password, master)
	if err != nil {
		return nil, err
	}
	if err := be.Save(ctx, "keys/"+keyID, bytesReader(keyBlob), int64(len(keyBlob))); err != nil {
		return nil, err
	}

	r := newRepository(be, cfg, master)
	r.index = newIndex(r.indexKey)
	return r, nil
}

// Open opens an existing repository, unlocking it with password and loading all
// index files into memory.
func Open(ctx context.Context, be backend.Backend, password []byte) (*Repository, error) {
	cfgBytes, err := readAll(ctx, be, "config")
	if err != nil {
		if backend.IsNotExist(err) {
			return nil, errors.New("repo: no repository at destination (missing config)")
		}
		return nil, err
	}
	var cfg repoConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		return nil, fmt.Errorf("repo: corrupt config: %w", err)
	}
	master, err := loadMasterKey(ctx, be, password)
	if err != nil {
		return nil, err
	}
	r := newRepository(be, cfg, master)
	ix, err := loadIndex(ctx, be, r.indexKey)
	if err != nil {
		return nil, err
	}
	r.index = ix
	return r, nil
}

func newRepository(be backend.Backend, cfg repoConfig, master []byte) *Repository {
	return &Repository{
		be:       be,
		cfg:      cfg,
		master:   master,
		dataKey:  crypto.DeriveSubKey(master, ctxDataKey),
		indexKey: crypto.DeriveSubKey(master, ctxIndexKey),
		snapKey:  crypto.DeriveSubKey(master, ctxSnapKey),
		chunkKey: chunker.ChunkerKey(master),
	}
}

// ChunkerKey returns the keyed-FastCDC key the archiver must use.
func (r *Repository) ChunkerKey() []byte { return r.chunkKey }

// Index exposes the in-memory index (read-only helpers) for dedup queries.
func (r *Repository) Index() *Index { return r.index }

// Backend returns the underlying backend (for advanced callers).
func (r *Repository) Backend() backend.Backend { return r.be }

// HasBlob reports whether a blob id already exists in the repository.
func (r *Repository) HasBlob(id string) bool { return r.index.Has(id) }

// SaveBlob deduplicates, compresses, encrypts and packs a plaintext blob. It
// returns the blob id (hex BLAKE3). If the blob already exists it is a no-op.
// isNew reports whether the blob was newly stored (vs. deduplicated).
// Safe for concurrent use.
func (r *Repository) SaveBlob(ctx context.Context, typ BlobType, plaintext []byte) (id string, isNew bool, err error) {
	h := crypto.HashChunk(plaintext)
	id = hex.EncodeToString(h[:])
	if r.index.Has(id) {
		return id, false, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Re-check under lock (another goroutine may have added it), including blobs
	// already sitting in the open, not-yet-flushed pack.
	if r.index.Has(id) {
		return id, false, nil
	}
	if _, ok := r.pendingSet[id]; ok {
		return id, false, nil
	}

	if r.pack == nil {
		pw, err := newPackWriter(r.dataKey)
		if err != nil {
			return "", false, err
		}
		r.pack = pw
	}
	if r.pendingSet == nil {
		r.pendingSet = make(map[string]struct{})
	}

	entry, err := r.pack.addBlob(h, typ, plaintext)
	if err != nil {
		return "", false, err
	}
	// The pack offset recorded now is relative to the pack start; the index
	// gets the final PackID once flushed. Until then, record a provisional
	// entry keyed to the current pack via flushLocked's fixup.
	r.pending = append(r.pending, pendingBlob{id: id, entry: entry})
	r.pendingSet[id] = struct{}{}
	r.dirtyIx = true

	if r.pack.size() >= packTargetSize {
		if err := r.flushLocked(ctx); err != nil {
			return "", false, err
		}
	}
	return id, true, nil
}

// pendingBlob holds a blob added to the current (unflushed) pack; its final
// index location is known only once the pack id is assigned at flush time.
type pendingBlob struct {
	id    string
	entry blobEntry
}

// flushLocked finalizes the open pack, uploads it, and records index entries.
// Caller must hold r.mu.
func (r *Repository) flushLocked(ctx context.Context) error {
	if r.pack == nil || r.pack.empty() {
		return nil
	}
	packID, packBytes, _, err := r.pack.finalize()
	if err != nil {
		return err
	}
	if err := r.be.Save(ctx, PackKey(packID), bytesReader(packBytes), int64(len(packBytes))); err != nil {
		return err
	}
	for _, pb := range r.pending {
		r.index.Add(pb.id, IndexLocation{
			PackID:          packID,
			Offset:          pb.entry.Offset,
			Length:          pb.entry.Length,
			Type:            pb.entry.Type,
			UncompressedLen: pb.entry.UncompressedLen,
		})
	}
	r.pending = nil
	r.pendingSet = nil
	r.pack.reset()
	return nil
}

// LoadBlob fetches, decrypts and decompresses the blob with the given id.
func (r *Repository) LoadBlob(ctx context.Context, id string) ([]byte, error) {
	loc, ok := r.index.Lookup(id)
	if !ok {
		return nil, fmt.Errorf("repo: blob %s not found in index", short(id))
	}
	rc, err := r.be.Load(ctx, PackKey(loc.PackID), loc.Offset, loc.Length)
	if err != nil {
		return nil, err
	}
	sealed, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, err
	}
	plain, err := decodeBlob(r.dataKey, sealed)
	if err != nil {
		return nil, fmt.Errorf("repo: decode blob %s: %w", short(id), err)
	}
	return plain, nil
}

// SaveTree serializes and stores a directory tree, returning its blob id.
func (r *Repository) SaveTree(ctx context.Context, nodes []Node) (string, error) {
	b, err := marshalTree(nodes)
	if err != nil {
		return "", err
	}
	id, _, err := r.SaveBlob(ctx, BlobTree, b)
	return id, err
}

// LoadTree fetches and parses a directory tree blob.
func (r *Repository) LoadTree(ctx context.Context, id string) ([]Node, error) {
	b, err := r.LoadBlob(ctx, id)
	if err != nil {
		return nil, err
	}
	return unmarshalTree(b)
}

// Flush flushes any open pack and persists the in-memory index if it changed.
func (r *Repository) Flush(ctx context.Context) error {
	r.mu.Lock()
	if err := r.flushLocked(ctx); err != nil {
		r.mu.Unlock()
		return err
	}
	dirty := r.dirtyIx
	r.dirtyIx = false
	r.mu.Unlock()
	if dirty {
		if _, err := r.index.Save(ctx, r.be); err != nil {
			return err
		}
	}
	return nil
}

// SaveSnapshot stores a snapshot record and returns its id. Callers should
// Flush the repository before (or SaveSnapshot after Flush) to ensure all
// referenced blobs and the index are persisted.
func (r *Repository) SaveSnapshot(ctx context.Context, snap *Snapshot) (string, error) {
	snap.Version = repoVersion
	return saveSnapshot(ctx, r.be, r.snapKey, snap)
}

// ListSnapshots returns all snapshots, newest first.
func (r *Repository) ListSnapshots(ctx context.Context) ([]*Snapshot, error) {
	return listSnapshots(ctx, r.be, r.snapKey)
}

// LoadSnapshot loads a snapshot by its full id.
func (r *Repository) LoadSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	return loadSnapshot(ctx, r.be, r.snapKey, id)
}

// FindSnapshot resolves a possibly-abbreviated snapshot id (>=1 hex char) to a
// full snapshot, erroring on ambiguity or no match.
func (r *Repository) FindSnapshot(ctx context.Context, prefix string) (*Snapshot, error) {
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	var match *Snapshot
	for _, s := range snaps {
		if strings.HasPrefix(s.ID, prefix) {
			if match != nil {
				return nil, fmt.Errorf("repo: snapshot id %q is ambiguous", prefix)
			}
			match = s
		}
	}
	if match == nil {
		return nil, fmt.Errorf("repo: no snapshot matching %q", prefix)
	}
	return match, nil
}

// Config exposes repository config metadata.
func (r *Repository) Config() (id string, version int) { return r.cfg.ID, r.cfg.Version }

// Close flushes and closes the underlying backend.
func (r *Repository) Close(ctx context.Context) error {
	err := r.Flush(ctx)
	if r.pack != nil {
		r.pack.close()
	}
	if cerr := r.be.Close(); err == nil {
		err = cerr
	}
	return err
}

// --- small helpers ---

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func hexEncode(b []byte) string { return hex.EncodeToString(b) }

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
