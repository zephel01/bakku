// Package retry provides a small exponential-backoff retry helper shared by
// all remote storage backends (s3, sftp, gdrive, dropbox, smb).
package retry

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

// DefaultAttempts is the number of attempts made by Do (the initial try plus
// retries), matching the "3 tries total" requirement.
const DefaultAttempts = 3

// baseDelay is the base of the exponential backoff: base * 2^n.
const baseDelay = 200 * time.Millisecond

// Do calls fn up to DefaultAttempts times, sleeping with exponential backoff
// (200ms * 2^n plus jitter) between attempts. It stops early and returns nil
// as soon as fn succeeds. If ctx is cancelled (either before a call or while
// sleeping), Do returns ctx.Err() immediately. If all attempts fail, Do
// returns the last error observed.
func Do(ctx context.Context, fn func(ctx context.Context) error) error {
	return DoN(ctx, DefaultAttempts, fn)
}

// permanentError wraps an error to signal that Do/DoN must not retry it.
// Use Permanent to construct one, and Unwrap (via errors.Unwrap/errors.Is/As)
// to recover the original error from the value Do returns.
type permanentError struct{ err error }

func (p *permanentError) Error() string { return p.err.Error() }
func (p *permanentError) Unwrap() error { return p.err }

// Permanent wraps err so that Do/DoN stop retrying immediately and return
// (the unwrapped) err, instead of exhausting all attempts. Use this for
// errors that are known not to be transient, such as "not found" or
// "permission denied" responses from a remote backend.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// DoN is like Do but allows overriding the number of attempts. attempts must
// be >= 1; values < 1 are treated as 1.
func DoN(ctx context.Context, attempts int, fn func(ctx context.Context) error) error {
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for n := 0; n < attempts; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
		var perm *permanentError
		if errors.As(lastErr, &perm) {
			return perm.err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if n == attempts-1 {
			break
		}

		delay := backoff(n)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

// backoff returns base*2^n plus up to base/2 of jitter, for n starting at 0.
func backoff(n int) time.Duration {
	d := baseDelay << uint(n)
	jitter := time.Duration(rand.Int63n(int64(baseDelay/2) + 1))
	return d + jitter
}
