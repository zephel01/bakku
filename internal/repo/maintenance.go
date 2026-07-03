package repo

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/zephel01/bakku/internal/crypto"
)

// This file adds the maintenance-oriented public API used by the forget / prune
// / check commands: enumerating packs, reading a pack's blob catalogue,
// deleting snapshots, repacking a subset of blobs, and rebuilding the index.

// PackInfo describes a stored pack and the blobs it holds (from the on-disk
// pack header, not the in-memory index).
type PackInfo struct {
	ID      string      // pack id (== storage key suffix)
	Size    int64       // total pack size in bytes
	Entries []BlobEntry // blobs contained, in storage order
}

// BlobEntry is the exported view of a blob's location within a pack.
type BlobEntry struct {
	ID              string
	Type            BlobType
	Offset          int64
	Length          int64
	UncompressedLen int64
}

// ListPacks enumerates all pack ids under data/ with their byte sizes. It does
// not read pack contents.
func (r *Repository) ListPacks(ctx context.Context) ([]PackInfo, error) {
	var packs []PackInfo
	err := r.be.List(ctx, "data", func(k string, size int64) error {
		id := packIDFromKey(k)
		if id == "" {
			return nil
		}
		packs = append(packs, PackInfo{ID: id, Size: size})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return packs, nil
}

// packIDFromKey extracts the pack id (last path segment) from a data/ key.
func packIDFromKey(k string) string {
	for i := len(k) - 1; i >= 0; i-- {
		if k[i] == '/' {
			return k[i+1:]
		}
	}
	return k
}

// ReadPackEntries downloads a pack and returns its blob catalogue by decrypting
// the trailing header. Used by check/prune to learn a pack's contents
// independently of the index.
func (r *Repository) ReadPackEntries(ctx context.Context, packID string) ([]BlobEntry, error) {
	packBytes, err := r.readWholePack(ctx, packID)
	if err != nil {
		return nil, err
	}
	entries, err := readPackHeader(r.dataKey, packBytes)
	if err != nil {
		return nil, fmt.Errorf("repo: pack %s: %w", short(packID), err)
	}
	return toBlobEntries(entries), nil
}

func toBlobEntries(in []blobEntry) []BlobEntry {
	out := make([]BlobEntry, len(in))
	for i, e := range in {
		out[i] = BlobEntry{
			ID:              e.ID,
			Type:            e.Type,
			Offset:          e.Offset,
			Length:          e.Length,
			UncompressedLen: e.UncompressedLen,
		}
	}
	return out
}

// readWholePack loads the entire pack file into memory.
func (r *Repository) readWholePack(ctx context.Context, packID string) ([]byte, error) {
	rc, err := r.be.Load(ctx, PackKey(packID), 0, -1)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// VerifyPack reads a pack and, for each blob, decrypts+decompresses it and
// recomputes its BLAKE3 id, checking it matches the recorded id. It returns a
// slice of human-readable errors (empty if the pack verifies clean).
func (r *Repository) VerifyPack(ctx context.Context, packID string) []error {
	packBytes, err := r.readWholePack(ctx, packID)
	if err != nil {
		return []error{fmt.Errorf("pack %s: load: %w", short(packID), err)}
	}
	entries, err := readPackHeader(r.dataKey, packBytes)
	if err != nil {
		return []error{fmt.Errorf("pack %s: header: %w", short(packID), err)}
	}
	var errs []error
	for _, e := range entries {
		end := e.Offset + e.Length
		if e.Offset < 0 || end > int64(len(packBytes)) {
			errs = append(errs, fmt.Errorf("pack %s: blob %s: offset/length out of range", short(packID), short(e.ID)))
			continue
		}
		plain, err := decodeBlob(r.dataKey, packBytes[e.Offset:end])
		if err != nil {
			errs = append(errs, fmt.Errorf("pack %s: blob %s: decode: %w", short(packID), short(e.ID), err))
			continue
		}
		h := crypto.HashChunk(plain)
		if got := hex.EncodeToString(h[:]); got != e.ID {
			errs = append(errs, fmt.Errorf("pack %s: blob %s: hash mismatch (recomputed %s)", short(packID), short(e.ID), short(got)))
		}
	}
	return errs
}

// DeleteSnapshot removes the snapshot record with the given full id. It does not
// touch any blobs (run prune to reclaim unreferenced blobs afterward).
func (r *Repository) DeleteSnapshot(ctx context.Context, id string) error {
	return r.be.Delete(ctx, SnapshotKey(id))
}

// IndexEntries returns a copy of every (blobID -> location) mapping currently in
// the in-memory index.
func (r *Repository) IndexEntries() map[string]IndexLocation {
	return r.index.All()
}

// All returns a snapshot copy of all index entries.
func (ix *Index) All() map[string]IndexLocation {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	m := make(map[string]IndexLocation, len(ix.m))
	for k, v := range ix.m {
		m[k] = v
	}
	return m
}

// LoadBlobFromPack reads a single blob directly from a specific pack location,
// bypassing the index. Used during repack when the working index may be in flux.
func (r *Repository) LoadBlobFromPack(ctx context.Context, loc IndexLocation) ([]byte, error) {
	rc, err := r.be.Load(ctx, PackKey(loc.PackID), loc.Offset, loc.Length)
	if err != nil {
		return nil, err
	}
	sealed, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, err
	}
	return decodeBlob(r.dataKey, sealed)
}

// Repack writes the given blobs (keyed by id -> current location) into one or
// more fresh packs and records their new locations in the provided index map.
// It returns the ids of the newly written packs. The source blobs are read via
// their current locations; the new index map is updated in place so the caller
// can persist it. Repack does NOT delete anything.
func (r *Repository) Repack(ctx context.Context, blobs map[string]IndexLocation, newIndex map[string]IndexLocation) ([]string, error) {
	var newPacks []string
	pw, err := newPackWriter(r.dataKey)
	if err != nil {
		return nil, err
	}
	defer pw.close()

	flush := func() error {
		if pw.empty() {
			return nil
		}
		packID, packBytes, entries, err := pw.finalize()
		if err != nil {
			return err
		}
		if err := r.be.Save(ctx, PackKey(packID), bytesReader(packBytes), int64(len(packBytes))); err != nil {
			return err
		}
		for _, e := range entries {
			newIndex[e.ID] = IndexLocation{
				PackID:          packID,
				Offset:          e.Offset,
				Length:          e.Length,
				Type:            e.Type,
				UncompressedLen: e.UncompressedLen,
			}
		}
		newPacks = append(newPacks, packID)
		pw.reset()
		return nil
	}

	for id, loc := range blobs {
		plain, err := r.LoadBlobFromPack(ctx, loc)
		if err != nil {
			return newPacks, fmt.Errorf("repo: repack read blob %s: %w", short(id), err)
		}
		h := crypto.HashChunk(plain)
		if _, err := pw.addBlob(h, loc.Type, plain); err != nil {
			return newPacks, err
		}
		if pw.size() >= packTargetSize {
			if err := flush(); err != nil {
				return newPacks, err
			}
		}
	}
	if err := flush(); err != nil {
		return newPacks, err
	}
	return newPacks, nil
}

// DeletePack removes a pack file from the backend.
func (r *Repository) DeletePack(ctx context.Context, packID string) error {
	return r.be.Delete(ctx, PackKey(packID))
}

// WriteIndex persists the given (id -> location) map as a single new index file
// and returns its id. It does not touch existing index files.
func (r *Repository) WriteIndex(ctx context.Context, m map[string]IndexLocation) (string, error) {
	ix := newIndex(r.indexKey)
	ix.m = m
	return ix.Save(ctx, r.be)
}

// ListIndexFiles returns the storage keys of all index/ files.
func (r *Repository) ListIndexFiles(ctx context.Context) ([]string, error) {
	var keys []string
	err := r.be.List(ctx, "index", func(k string, _ int64) error {
		keys = append(keys, k)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// DeleteIndexFile removes an index file by its full storage key.
func (r *Repository) DeleteIndexFile(ctx context.Context, key string) error {
	return r.be.Delete(ctx, key)
}

// ReplaceIndex atomically-ish swaps the repository's index files: it writes the
// new index first, and only after that succeeds does it delete the old index
// files. This ordering guarantees that at no point is the repository left
// without a durable index describing the live blobs. On return, the in-memory
// index is replaced with m as well.
//
// oldKeys is the set of index/ keys that existed before the operation and should
// be removed. Callers must pass the keys captured before WriteIndex/Repack, so
// freshly written index files are not deleted.
func (r *Repository) ReplaceIndex(ctx context.Context, m map[string]IndexLocation, oldKeys []string) error {
	if _, err := r.WriteIndex(ctx, m); err != nil {
		return fmt.Errorf("repo: write new index: %w", err)
	}
	for _, k := range oldKeys {
		if err := r.be.Delete(ctx, k); err != nil {
			return fmt.Errorf("repo: delete old index %s: %w", k, err)
		}
	}
	// Refresh the in-memory index to reflect the new state.
	ix := newIndex(r.indexKey)
	ix.m = m
	r.index = ix
	return nil
}
