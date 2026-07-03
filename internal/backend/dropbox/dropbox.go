// Package dropbox implements a Backend backed by Dropbox.
//
// URL form:
//
//	dropbox://path
//
// path is a '/'-separated path (relative to the app/account root) under
// which repository keys are stored.
//
// Authentication uses a long-lived (or refreshable) OAuth2 access token read
// from the BAKKU_DROPBOX_TOKEN environment variable.
//
// Uploads larger than 150MB are automatically split into an upload session
// (start/append.../finish) since Dropbox's single-shot Upload endpoint is
// limited to 150MB per request.
package dropbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"

	"github.com/zephel01/bakku/internal/backend/retry"
)

// errNotExist mirrors backend.ErrNotExist without importing the parent
// package (which would create an import cycle).
var errNotExist = os.ErrNotExist

// uploadSessionThreshold is the point above which Save switches from a
// single Upload call to an upload session (Dropbox's hard single-request
// cap is 150MB; we split at the same boundary). Declared as a var (not a
// const) so tests can shrink it to exercise the chunking logic without
// uploading 150MB of fixture data.
var uploadSessionThreshold int64 = 150 * 1024 * 1024

// uploadChunkSize is the amount of data sent per UploadSessionAppendV2 call
// when a file exceeds uploadSessionThreshold. Also a var for the same
// testing reason as uploadSessionThreshold.
var uploadChunkSize = 32 * 1024 * 1024

// filesAPI is the subset of the Dropbox Files API that Dropbox depends on.
// It exists so tests can substitute a fake implementation without any
// network access.
type filesAPI interface {
	Upload(arg *files.UploadArg, content io.Reader) (*files.FileMetadata, error)
	UploadSessionStart(arg *files.UploadSessionStartArg, content io.Reader) (*files.UploadSessionStartResult, error)
	UploadSessionAppendV2(arg *files.UploadSessionAppendArg, content io.Reader) error
	UploadSessionFinish(arg *files.UploadSessionFinishArg, content io.Reader) (*files.FileMetadata, error)
	Download(arg *files.DownloadArg) (*files.FileMetadata, io.ReadCloser, error)
	GetMetadata(arg *files.GetMetadataArg) (files.IsMetadata, error)
	Delete(arg *files.DeleteArg) (files.IsMetadata, error)
	ListFolder(arg *files.ListFolderArg) (*files.ListFolderResult, error)
	ListFolderContinue(arg *files.ListFolderContinueArg) (*files.ListFolderResult, error)
}

// Dropbox stores repository keys as files under a root path in a Dropbox
// account/app folder.
type Dropbox struct {
	api  filesAPI
	root string // '/'-prefixed, no trailing slash (e.g. "" or "/backups")
}

// ParseURL parses "dropbox://path" into a Dropbox-API-style path: '/'-prefixed,
// no trailing slash, "" meaning the root.
func ParseURL(raw string) (string, error) {
	rest := raw
	if strings.HasPrefix(rest, "dropbox://") {
		rest = strings.TrimPrefix(rest, "dropbox://")
	}
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", nil
	}
	return "/" + rest, nil
}

// New constructs a Dropbox backend from a destination string of the form
// "dropbox://path", authenticating with the token in BAKKU_DROPBOX_TOKEN.
func New(ctx context.Context, dst string) (*Dropbox, error) {
	root, err := ParseURL(dst)
	if err != nil {
		return nil, err
	}

	token := envToken()
	if token == "" {
		return nil, errors.New("dropbox: BAKKU_DROPBOX_TOKEN must be set to an access token")
	}

	cfg := dropbox.Config{Token: token}
	client := files.New(cfg)

	return newWithAPI(client, root), nil
}

func newWithAPI(api filesAPI, root string) *Dropbox {
	return &Dropbox{api: api, root: root}
}

func (d *Dropbox) fullPath(key string) string {
	key = strings.TrimPrefix(key, "/")
	if d.root == "" {
		return "/" + key
	}
	return d.root + "/" + key
}

func overwriteMode() *files.WriteMode {
	return &files.WriteMode{Tagged: dropbox.Tagged{Tag: files.WriteModeOverwrite}}
}

// Save uploads r to key, splitting into an upload session if the payload
// exceeds uploadSessionThreshold.
func (d *Dropbox) Save(ctx context.Context, key string, r io.Reader, size int64) error {
	p := d.fullPath(key)

	if size >= 0 && size <= uploadSessionThreshold {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		return retry.Do(ctx, func(ctx context.Context) error {
			commit := files.NewCommitInfo(p)
			commit.Mode = overwriteMode()
			arg := &files.UploadArg{CommitInfo: *commit}
			_, err := d.api.Upload(arg, bytesReader(data))
			return err
		})
	}

	return d.saveViaUploadSession(ctx, p, r)
}

// saveViaUploadSession uploads r in uploadChunkSize pieces via Dropbox's
// upload-session API (start, zero-or-more append, finish). Used for
// payloads over 150MB, and also as a fallback when size is unknown (-1),
// since we can't safely assume it's small.
//
// Protocol: read chunks of uploadChunkSize. The first chunk (even if it's
// also the last, i.e. the whole file is small) starts the session. Every
// subsequent chunk except the last is appended. The last chunk (which may
// be the first, or may be empty if the size is an exact multiple of the
// chunk size) finishes the session and commits the file.
func (d *Dropbox) saveViaUploadSession(ctx context.Context, p string, r io.Reader) error {
	buf := make([]byte, uploadChunkSize)

	readChunk := func() (chunk []byte, isLast bool, err error) {
		n, err := io.ReadFull(r, buf)
		if err != nil {
			if err == io.ErrUnexpectedEOF || err == io.EOF {
				return buf[:n], true, nil
			}
			return nil, false, err
		}
		return buf[:n], false, nil
	}

	firstChunk, isLast, err := readChunk()
	if err != nil {
		return err
	}

	var sessionID string
	err = retry.Do(ctx, func(ctx context.Context) error {
		arg := files.NewUploadSessionStartArg()
		arg.Close = isLast
		res, err := d.api.UploadSessionStart(arg, bytesReader(firstChunk))
		if err != nil {
			return err
		}
		sessionID = res.SessionId
		return nil
	})
	if err != nil {
		return err
	}
	offset := uint64(len(firstChunk))

	if isLast {
		return d.finishUploadSession(ctx, sessionID, offset, p, nil)
	}

	for {
		chunk, last, err := readChunk()
		if err != nil {
			return err
		}
		if last {
			return d.finishUploadSession(ctx, sessionID, offset, p, chunk)
		}

		curOffset := offset
		curChunk := chunk
		err = retry.Do(ctx, func(ctx context.Context) error {
			cursor := files.NewUploadSessionCursor(sessionID, curOffset)
			arg := files.NewUploadSessionAppendArg(cursor)
			return d.api.UploadSessionAppendV2(arg, bytesReader(curChunk))
		})
		if err != nil {
			return err
		}
		offset += uint64(len(chunk))
	}
}

func (d *Dropbox) finishUploadSession(ctx context.Context, sessionID string, offset uint64, p string, lastChunk []byte) error {
	return retry.Do(ctx, func(ctx context.Context) error {
		cursor := files.NewUploadSessionCursor(sessionID, offset)
		commit := files.NewCommitInfo(p)
		commit.Mode = overwriteMode()
		arg := files.NewUploadSessionFinishArg(cursor, commit)
		_, err := d.api.UploadSessionFinish(arg, bytesReader(lastChunk))
		return err
	})
}

// Load returns a reader for [offset, offset+length) of key.
func (d *Dropbox) Load(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	p := d.fullPath(key)
	var rc io.ReadCloser
	err := retry.Do(ctx, func(ctx context.Context) error {
		arg := files.NewDownloadArg(p)
		if rng := rangeHeader(offset, length); rng != "" {
			arg.ExtraHeaders = map[string]string{"Range": rng}
		}
		_, content, err := d.api.Download(arg)
		if err != nil {
			if isNotFound(err) {
				return retry.Permanent(fmt.Errorf("%w: %s", errNotExist, key))
			}
			return err
		}
		rc = content
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rc, nil
}

// Stat returns the size of key.
func (d *Dropbox) Stat(ctx context.Context, key string) (int64, error) {
	p := d.fullPath(key)
	var size int64
	err := retry.Do(ctx, func(ctx context.Context) error {
		arg := files.NewGetMetadataArg(p)
		meta, err := d.api.GetMetadata(arg)
		if err != nil {
			if isNotFound(err) {
				return retry.Permanent(fmt.Errorf("%w: %s", errNotExist, key))
			}
			return err
		}
		fm, ok := meta.(*files.FileMetadata)
		if !ok {
			return retry.Permanent(fmt.Errorf("%w: %s (not a file)", errNotExist, key))
		}
		size = int64(fm.Size)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return size, nil
}

// List calls fn for every file under prefix (recursively).
func (d *Dropbox) List(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	p := d.fullPath(prefix)
	if p == "/" {
		p = ""
	}

	var res *files.ListFolderResult
	err := retry.Do(ctx, func(ctx context.Context) error {
		arg := files.NewListFolderArg(p)
		arg.Recursive = true
		r, err := d.api.ListFolder(arg)
		if err != nil {
			if isNotFound(err) {
				return retry.Permanent(errNotExist)
			}
			return err
		}
		res = r
		return nil
	})
	if err != nil {
		if errors.Is(err, errNotExist) {
			return nil
		}
		return err
	}

	for {
		for _, entry := range res.Entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			fm, ok := entry.(*files.FileMetadata)
			if !ok {
				continue // skip folders
			}
			key := strings.TrimPrefix(fm.PathDisplay, d.root)
			key = strings.TrimPrefix(key, "/")
			if err := fn(key, int64(fm.Size)); err != nil {
				return err
			}
		}
		if !res.HasMore {
			return nil
		}
		cursor := res.Cursor
		err := retry.Do(ctx, func(ctx context.Context) error {
			arg := files.NewListFolderContinueArg(cursor)
			r, err := d.api.ListFolderContinue(arg)
			if err != nil {
				return err
			}
			res = r
			return nil
		})
		if err != nil {
			return err
		}
	}
}

// Delete removes key. A missing key is not an error.
func (d *Dropbox) Delete(ctx context.Context, key string) error {
	p := d.fullPath(key)
	return retry.Do(ctx, func(ctx context.Context) error {
		arg := files.NewDeleteArg(p)
		_, err := d.api.Delete(arg)
		if err != nil && !isNotFound(err) {
			return err
		}
		return nil
	})
}

// Close is a no-op for the Dropbox backend.
func (d *Dropbox) Close() error { return nil }

func envToken() string {
	return os.Getenv("BAKKU_DROPBOX_TOKEN")
}

func rangeHeader(offset, length int64) string {
	if offset == 0 && length < 0 {
		return ""
	}
	if length < 0 {
		return fmt.Sprintf("bytes=%d-", offset)
	}
	return fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
}

// isNotFound reports whether err indicates a missing path, matching on the
// Dropbox SDK's tagged LookupError/GetMetadataError structures embedded in
// its APIError wrapper.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not_found") || strings.Contains(msg, "path/not_found")
}

func bytesReader(b []byte) io.Reader { return &byteReader{b: b} }

type byteReader struct {
	b   []byte
	pos int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}
