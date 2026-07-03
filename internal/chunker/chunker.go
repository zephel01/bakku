// Package chunker wraps PlakarKorp/go-cdc-chunkers to provide keyed FastCDC
// content-defined chunking with bakku's fixed size parameters.
package chunker

import (
	"errors"
	"io"

	chunkers "github.com/PlakarKorp/go-cdc-chunkers"
	// register the fastcdc algorithms (fastcdc, kfastcdc, ...).
	_ "github.com/PlakarKorp/go-cdc-chunkers/chunkers/fastcdc"

	"github.com/zephel01/bakku/internal/crypto"
)

const (
	// MinSize is the minimum chunk size (512 KiB).
	MinSize = 512 * 1024
	// NormalSize is the target/normal chunk size (1 MiB). Must be a power of two.
	NormalSize = 1024 * 1024
	// MaxSize is the maximum chunk size (8 MiB).
	MaxSize = 8 * 1024 * 1024

	// algorithm is the keyed FastCDC algorithm registered by the fastcdc package.
	algorithm = "kfastcdc"

	// chunkerKeyContext is the BLAKE3 derive-key context for the chunker key.
	// Keeping the Gear-table key distinct from encryption keys avoids leaking
	// chunk-boundary structure across independent security domains.
	chunkerKeyContext = "github.com/zephel01/bakku 2026 chunker gear key"
)

// ChunkerKey derives the keyed-FastCDC gear-table key from the master key.
func ChunkerKey(masterKey []byte) []byte {
	return crypto.DeriveSubKey(masterKey, chunkerKeyContext)
}

// Chunker splits a stream into content-defined chunks.
type Chunker struct {
	c *chunkers.Chunker
}

// New returns a Chunker reading from r, using keyed FastCDC with the given key.
// The key must be non-nil (32 bytes recommended); it seeds the Gear table so
// chunk boundaries are not predictable from the plaintext alone.
func New(r io.Reader, key []byte) (*Chunker, error) {
	if len(key) == 0 {
		return nil, errors.New("chunker: key is required for keyed FastCDC")
	}
	opts := &chunkers.ChunkerOpts{
		MinSize:    MinSize,
		NormalSize: NormalSize,
		MaxSize:    MaxSize,
		Key:        key,
	}
	c, err := chunkers.NewChunker(algorithm, r, opts)
	if err != nil {
		return nil, err
	}
	return &Chunker{c: c}, nil
}

// Next returns the next chunk. It returns io.EOF (with a possibly-non-empty
// final chunk) when the stream is exhausted. Callers MUST copy the returned
// slice if they need to retain it: it aliases the chunker's internal buffer and
// is invalidated by the next call to Next.
func (c *Chunker) Next() ([]byte, error) {
	return c.c.Next()
}

// Split calls fn for every chunk in the stream. The chunk slice passed to fn is
// only valid for the duration of the call.
func (c *Chunker) Split(fn func(chunk []byte) error) error {
	for {
		chunk, err := c.c.Next()
		if err != nil && err != io.EOF {
			return err
		}
		if len(chunk) > 0 {
			if e := fn(chunk); e != nil {
				return e
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}
