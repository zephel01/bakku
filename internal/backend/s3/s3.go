// Package s3 implements a Backend backed by Amazon S3 (or any S3-compatible
// object store reachable via a custom endpoint).
//
// URL form:
//
//	s3://bucket/prefix?endpoint=https://minio.example.com&region=us-east-1
//
// Credentials are resolved via the standard AWS SDK v2 credential chain
// (environment variables, shared config/credentials files, EC2/ECS instance
// metadata, etc.) — no bakku-specific credential options are supported.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"github.com/zephel01/bakku/internal/backend/keyguard"
	"github.com/zephel01/bakku/internal/backend/retry"
)

// errNotExist mirrors backend.ErrNotExist without importing the parent
// package (which would create an import cycle).
var errNotExist = os.ErrNotExist

// S3 stores repository keys as objects in an S3 bucket under an optional
// key prefix.
type S3 struct {
	client *s3.Client
	bucket string
	prefix string // '/'-separated, no leading/trailing slash; "" for none
}

// ParsedURL holds the components extracted from an s3:// destination URL.
type ParsedURL struct {
	Bucket   string
	Prefix   string
	Endpoint string
	Region   string
}

// ParseURL parses a "s3://bucket/prefix?endpoint=...&region=..." destination
// string. The scheme itself ("s3://") must already be stripped by the caller
// UNLESS the full URL (including scheme) is passed — both forms are
// accepted for convenience.
func ParseURL(raw string) (ParsedURL, error) {
	full := raw
	if !strings.Contains(full, "://") {
		full = "s3://" + full
	}
	u, err := url.Parse(full)
	if err != nil {
		return ParsedURL{}, fmt.Errorf("s3: invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "" && u.Scheme != "s3" {
		return ParsedURL{}, fmt.Errorf("s3: unexpected scheme %q", u.Scheme)
	}
	bucket := u.Host
	if bucket == "" {
		return ParsedURL{}, fmt.Errorf("s3: missing bucket in %q", raw)
	}
	prefix := strings.Trim(u.Path, "/")
	q := u.Query()
	return ParsedURL{
		Bucket:   bucket,
		Prefix:   prefix,
		Endpoint: q.Get("endpoint"),
		Region:   q.Get("region"),
	}, nil
}

// New constructs an S3 backend from a destination string of the form
// "s3://bucket/prefix?endpoint=...&region=...".
func New(ctx context.Context, dst string) (*S3, error) {
	return newWithOptFns(ctx, dst)
}

// newWithOptFns is New plus additional s3.Options overrides, used by tests to
// inject a fake HTTPClient pointed at an httptest server.
func newWithOptFns(ctx context.Context, dst string, extra ...func(*s3.Options)) (*S3, error) {
	pu, err := ParseURL(dst)
	if err != nil {
		return nil, err
	}

	var optFns []func(*config.LoadOptions) error
	if pu.Region != "" {
		optFns = append(optFns, config.WithRegion(pu.Region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("s3: loading AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if pu.Endpoint != "" {
			o.BaseEndpoint = aws.String(pu.Endpoint)
			o.UsePathStyle = true
		}
		// Work around SDK v1.73+ defaulting to always-compute checksums,
		// which some S3-compatible providers reject.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		for _, fn := range extra {
			fn(o)
		}
	})

	return &S3{client: client, bucket: pu.Bucket, prefix: pu.Prefix}, nil
}

func (s *S3) objectKey(key string) string {
	key = strings.TrimPrefix(key, "/")
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

// Save uploads r to key. size may be -1; the SDK will buffer as needed.
func (s *S3) Save(ctx context.Context, key string, r io.Reader, size int64) error {
	if err := keyguard.Validate(key); err != nil {
		return err
	}
	// PutObject requires a body that can be read exactly once per attempt.
	// If the source isn't seekable and we need to retry, we must buffer.
	body, err := toReaderAt(r, size)
	if err != nil {
		return err
	}

	return retry.Do(ctx, func(ctx context.Context) error {
		reader, resetErr := body.reset()
		if resetErr != nil {
			return resetErr
		}
		input := &s3.PutObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.objectKey(key)),
			Body:   reader,
		}
		_, err := s.client.PutObject(ctx, input)
		return err
	})
}

// Load returns a reader for [offset, offset+length) of key.
func (s *S3) Load(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := keyguard.Validate(key); err != nil {
		return nil, err
	}
	var out *s3.GetObjectOutput
	err := retry.Do(ctx, func(ctx context.Context) error {
		input := &s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.objectKey(key)),
		}
		if rng := rangeHeader(offset, length); rng != "" {
			input.Range = aws.String(rng)
		}
		res, err := s.client.GetObject(ctx, input)
		if err != nil {
			if isNotFound(err) {
				return retry.Permanent(&notFoundErr{key: key})
			}
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		var nf *notFoundErr
		if errors.As(err, &nf) {
			return nil, fmt.Errorf("%w: %s", errNotExist, key)
		}
		return nil, err
	}
	return out.Body, nil
}

// Stat returns the size of key.
func (s *S3) Stat(ctx context.Context, key string) (int64, error) {
	if err := keyguard.Validate(key); err != nil {
		return 0, err
	}
	var size int64
	err := retry.Do(ctx, func(ctx context.Context) error {
		input := &s3.HeadObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.objectKey(key)),
		}
		out, err := s.client.HeadObject(ctx, input)
		if err != nil {
			if isNotFound(err) {
				return retry.Permanent(&notFoundErr{key: key})
			}
			return err
		}
		if out.ContentLength != nil {
			size = *out.ContentLength
		}
		return nil
	})
	if err != nil {
		var nf *notFoundErr
		if errors.As(err, &nf) {
			return 0, fmt.Errorf("%w: %s", errNotExist, key)
		}
		return 0, err
	}
	return size, nil
}

// List calls fn for every key under prefix.
func (s *S3) List(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	if err := keyguard.Validate(prefix); err != nil {
		return err
	}
	fullPrefix := s.objectKey(prefix)
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(fullPrefix),
	})

	stripLen := 0
	if s.prefix != "" {
		stripLen = len(s.prefix) + 1
	}

	for paginator.HasMorePages() {
		var page *s3.ListObjectsV2Output
		err := retry.Do(ctx, func(ctx context.Context) error {
			p, err := paginator.NextPage(ctx)
			if err != nil {
				return err
			}
			page = p
			return nil
		})
		if err != nil {
			return err
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			k := *obj.Key
			if stripLen > 0 && len(k) >= stripLen {
				k = k[stripLen:]
			}
			size := int64(0)
			if obj.Size != nil {
				size = *obj.Size
			}
			if err := fn(k, size); err != nil {
				return err
			}
		}
	}
	return nil
}

// Delete removes key. A missing key is not an error.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := keyguard.Validate(key); err != nil {
		return err
	}
	return retry.Do(ctx, func(ctx context.Context) error {
		_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.objectKey(key)),
		})
		// S3 DeleteObject is idempotent and does not error on a missing key,
		// but some S3-compatible backends may; treat NotFound as success.
		if err != nil && isNotFound(err) {
			return nil
		}
		return err
	})
}

// Close is a no-op for the S3 backend (the SDK client owns no persistent
// connections that need explicit closing).
func (s *S3) Close() error { return nil }

// rangeHeader builds an HTTP Range header value for [offset, offset+length).
// length<0 means "from offset to the end". offset==0 && length<0 means the
// whole object, in which case no Range header is needed.
func rangeHeader(offset, length int64) string {
	if offset == 0 && length < 0 {
		return ""
	}
	if length < 0 {
		return fmt.Sprintf("bytes=%d-", offset)
	}
	if length == 0 {
		// Zero-length reads are edge cases; request a single byte and let
		// the caller's LimitReader-equivalent handle discarding, but since
		// S3 has no direct zero-length range concept we approximate with
		// the empty range from offset to offset (still returns at least
		// nothing meaningful); simplest is to request just offset.
		return fmt.Sprintf("bytes=%d-%d", offset, offset)
	}
	return fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
}

// notFoundErr is a sentinel used internally to distinguish "not found" from
// other retryable errors inside retry.Do (so we don't keep retrying a 404).
type notFoundErr struct{ key string }

func (e *notFoundErr) Error() string { return "not found: " + e.key }

// isNotFound reports whether err indicates a missing S3 object/bucket.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

// seekableBody wraps a source reader so it can be replayed across retry
// attempts: if the source already implements io.Seeker (e.g. *os.File), we
// seek back to the start; otherwise we buffer the entire payload in memory
// on first use.
type seekableBody struct {
	seeker io.Seeker
	orig   io.Reader
	buf    []byte
	loaded bool
}

func toReaderAt(r io.Reader, size int64) (*seekableBody, error) {
	if sk, ok := r.(io.Seeker); ok {
		return &seekableBody{seeker: sk, orig: r}, nil
	}
	return &seekableBody{orig: r}, nil
}

func (b *seekableBody) reset() (io.Reader, error) {
	if b.seeker != nil {
		if _, err := b.seeker.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		return b.orig, nil
	}
	if !b.loaded {
		data, err := io.ReadAll(b.orig)
		if err != nil {
			return nil, err
		}
		b.buf = data
		b.loaded = true
	}
	return bytes.NewReader(b.buf), nil
}
