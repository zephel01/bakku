// Package gdrive implements a Backend backed by Google Drive.
//
// URL form:
//
//	gdrive://folder-path
//
// folder-path is a '/'-separated path of Drive folder names under "My
// Drive" that will be created on demand; repository keys are mapped onto
// this folder hierarchy (e.g. key "data/ab/cd" becomes a file named "cd" in
// folder-path/data/ab).
//
// Authentication uses OAuth2 (installed-app flow):
//   - BAKKU_GDRIVE_CREDENTIALS must point at an OAuth client-secret JSON file
//     (downloaded from Google Cloud Console).
//   - The obtained token is cached at ~/.config/bakku/gdrive-token.json
//     (mode 0600) and reused (and refreshed) on subsequent runs via
//     oauth2.ReuseTokenSource.
//   - On first use (no cached token), an authorization URL is printed and
//     the user is prompted to paste back the resulting code.
package gdrive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/zephel01/bakku/internal/backend/keyguard"
	"github.com/zephel01/bakku/internal/backend/retry"
)

// errNotExist mirrors backend.ErrNotExist without importing the parent
// package (which would create an import cycle).
var errNotExist = os.ErrNotExist

const folderMimeType = "application/vnd.google-apps.folder"

// driveAPI is the subset of the Drive v3 API that GDrive depends on. It
// exists so tests can substitute a fake implementation without any network
// access.
type driveAPI interface {
	CreateFile(ctx context.Context, name, parentID string, isFolder bool, content io.Reader) (id string, err error)
	FindChild(ctx context.Context, name, parentID string, isFolder bool) (id string, size int64, found bool, err error)
	Download(ctx context.Context, id string, offset, length int64) (io.ReadCloser, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, parentID string) ([]driveEntry, error)
}

type driveEntry struct {
	ID       string
	Name     string
	IsFolder bool
	Size     int64
}

// GDrive stores repository keys as files in a Google Drive folder hierarchy.
type GDrive struct {
	api      driveAPI
	rootPath string // the folder-path from the URL, '/'-separated, no leading/trailing slash

	mu         sync.Mutex
	folderID   map[string]string // '/'-separated relative folder path -> Drive folder ID
	rootIDOnce sync.Once
	rootID     string
	rootErr    error
}

// ParseURL parses "gdrive://folder-path" and returns the folder path with no
// leading/trailing slashes.
func ParseURL(raw string) (string, error) {
	rest := raw
	if strings.HasPrefix(rest, "gdrive://") {
		rest = strings.TrimPrefix(rest, "gdrive://")
	}
	rest = strings.Trim(rest, "/")
	return rest, nil
}

// New constructs a GDrive backend from a destination string of the form
// "gdrive://folder-path", authenticating via OAuth2 as described in the
// package doc.
func New(ctx context.Context, dst string) (*GDrive, error) {
	folderPath, err := ParseURL(dst)
	if err != nil {
		return nil, err
	}

	credPath := os.Getenv("BAKKU_GDRIVE_CREDENTIALS")
	if credPath == "" {
		return nil, errors.New("gdrive: BAKKU_GDRIVE_CREDENTIALS must be set to an OAuth client-secret JSON path")
	}
	credData, err := os.ReadFile(credPath)
	if err != nil {
		return nil, fmt.Errorf("gdrive: reading credentials %q: %w", credPath, err)
	}
	oauthCfg, err := google.ConfigFromJSON(credData, drive.DriveFileScope)
	if err != nil {
		return nil, fmt.Errorf("gdrive: parsing credentials %q: %w", credPath, err)
	}

	tokenPath, err := tokenCachePath()
	if err != nil {
		return nil, err
	}
	tok, err := loadOrObtainToken(ctx, oauthCfg, tokenPath)
	if err != nil {
		return nil, err
	}

	tokenSource := oauth2.ReuseTokenSource(tok, oauthCfg.TokenSource(ctx, tok))

	svc, err := drive.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		return nil, fmt.Errorf("gdrive: creating Drive service: %w", err)
	}

	return newWithAPI(&realDriveAPI{svc: svc}, folderPath), nil
}

func newWithAPI(api driveAPI, folderPath string) *GDrive {
	return &GDrive{
		api:      api,
		rootPath: folderPath,
		folderID: make(map[string]string),
	}
}

func tokenCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("gdrive: cannot locate home dir for token cache: %w", err)
	}
	return filepath.Join(home, ".config", "bakku", "gdrive-token.json"), nil
}

func loadOrObtainToken(ctx context.Context, cfg *oauth2.Config, tokenPath string) (*oauth2.Token, error) {
	if tok, err := readToken(tokenPath); err == nil {
		return tok, nil
	}

	authURL := cfg.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Fprintf(os.Stdout, "gdrive: open this URL in a browser to authorize bakku:\n\n%s\n\nEnter the authorization code: ", authURL)

	reader := bufio.NewReader(os.Stdin)
	code, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("gdrive: reading authorization code: %w", err)
	}
	code = strings.TrimSpace(code)

	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("gdrive: exchanging authorization code: %w", err)
	}

	if err := saveToken(tokenPath, tok); err != nil {
		// Not fatal: proceed with the in-memory token even if caching failed.
		fmt.Fprintf(os.Stderr, "gdrive: warning: could not cache token: %v\n", err)
	}
	return tok, nil
}

func readToken(path string) (*oauth2.Token, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func saveToken(path string, tok *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// splitKey splits a '/'-separated key into its directory components and
// final file name.
func splitKey(key string) (dirs []string, name string) {
	key = strings.Trim(key, "/")
	parts := strings.Split(key, "/")
	if len(parts) == 0 {
		return nil, ""
	}
	return parts[:len(parts)-1], parts[len(parts)-1]
}

// resolveRoot returns the Drive folder ID for the configured rootPath,
// creating intermediate folders as needed. Cached after first resolution.
func (g *GDrive) resolveRoot(ctx context.Context) (string, error) {
	g.rootIDOnce.Do(func() {
		id := "root"
		if g.rootPath != "" {
			for _, part := range strings.Split(g.rootPath, "/") {
				childID, err := g.ensureFolder(ctx, part, id)
				if err != nil {
					g.rootErr = err
					return
				}
				id = childID
			}
		}
		g.rootID = id
	})
	return g.rootID, g.rootErr
}

// ensureFolder finds-or-creates a folder named `name` under `parentID`,
// applying the retry helper to the remote calls.
func (g *GDrive) ensureFolder(ctx context.Context, name, parentID string) (string, error) {
	var id string
	err := retry.Do(ctx, func(ctx context.Context) error {
		foundID, _, found, err := g.api.FindChild(ctx, name, parentID, true)
		if err != nil {
			return err
		}
		if found {
			id = foundID
			return nil
		}
		createdID, err := g.api.CreateFile(ctx, name, parentID, true, nil)
		if err != nil {
			return err
		}
		id = createdID
		return nil
	})
	return id, err
}

// resolveDir walks dirs (relative to root) returning the folder ID for the
// deepest directory, creating folders as needed. Results are cached.
func (g *GDrive) resolveDir(ctx context.Context, dirs []string) (string, error) {
	rootID, err := g.resolveRoot(ctx)
	if err != nil {
		return "", err
	}

	cacheKey := strings.Join(dirs, "/")
	g.mu.Lock()
	if id, ok := g.folderID[cacheKey]; ok {
		g.mu.Unlock()
		return id, nil
	}
	g.mu.Unlock()

	id := rootID
	pathSoFar := ""
	for _, part := range dirs {
		if pathSoFar == "" {
			pathSoFar = part
		} else {
			pathSoFar = pathSoFar + "/" + part
		}

		g.mu.Lock()
		cached, ok := g.folderID[pathSoFar]
		g.mu.Unlock()
		if ok {
			id = cached
			continue
		}

		childID, err := g.ensureFolder(ctx, part, id)
		if err != nil {
			return "", err
		}
		id = childID
		g.mu.Lock()
		g.folderID[pathSoFar] = id
		g.mu.Unlock()
	}
	return id, nil
}

// Save uploads r as a file named by the last path component of key, inside
// the folder hierarchy given by the rest of key. Existing files with the
// same name are removed first so Save behaves like an atomic overwrite from
// the caller's perspective (Drive has no rename-based atomic replace across
// distinct file IDs in the general case, but delete+create keeps behavior
// consistent with "a key either fully exists or not").
func (g *GDrive) Save(ctx context.Context, key string, r io.Reader, size int64) error {
	if err := keyguard.Validate(key); err != nil {
		return err
	}
	dirs, name := splitKey(key)
	if name == "" {
		return fmt.Errorf("gdrive: invalid key %q", key)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	dirID, err := g.resolveDir(ctx, dirs)
	if err != nil {
		return err
	}

	return retry.Do(ctx, func(ctx context.Context) error {
		if existingID, _, found, err := g.api.FindChild(ctx, name, dirID, false); err == nil && found {
			_ = g.api.Delete(ctx, existingID)
		}
		_, err := g.api.CreateFile(ctx, name, dirID, false, bytesReader(data))
		return err
	})
}

// Load returns a reader for [offset, offset+length) of key.
func (g *GDrive) Load(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := keyguard.Validate(key); err != nil {
		return nil, err
	}
	dirs, name := splitKey(key)
	dirID, err := g.resolveDirReadOnly(ctx, dirs)
	if err != nil {
		if errors.Is(err, errNotExist) {
			return nil, fmt.Errorf("%w: %s", errNotExist, key)
		}
		return nil, err
	}

	var fileID string
	err = retry.Do(ctx, func(ctx context.Context) error {
		id, _, found, err := g.api.FindChild(ctx, name, dirID, false)
		if err != nil {
			return err
		}
		if !found {
			return retry.Permanent(fmt.Errorf("%w: %s", errNotExist, key))
		}
		fileID = id
		return nil
	})
	if err != nil {
		return nil, err
	}

	var rc io.ReadCloser
	err = retry.Do(ctx, func(ctx context.Context) error {
		r, err := g.api.Download(ctx, fileID, offset, length)
		if err != nil {
			return err
		}
		rc = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rc, nil
}

// resolveDirReadOnly is like resolveDir but returns errNotExist (rather than
// creating folders) when a directory component is missing — used by
// read-only operations (Load/Stat/List) so they don't have the side effect
// of creating folder structure that doesn't exist yet.
func (g *GDrive) resolveDirReadOnly(ctx context.Context, dirs []string) (string, error) {
	id, err := g.resolveRootReadOnly(ctx)
	if err != nil {
		return "", err
	}
	for _, part := range dirs {
		childID, _, found, err := g.api.FindChild(ctx, part, id, true)
		if err != nil {
			return "", err
		}
		if !found {
			return "", errNotExist
		}
		id = childID
	}
	return id, nil
}

func (g *GDrive) resolveRootReadOnly(ctx context.Context) (string, error) {
	if g.rootPath == "" {
		return "root", nil
	}
	id := "root"
	for _, part := range strings.Split(g.rootPath, "/") {
		childID, _, found, err := g.api.FindChild(ctx, part, id, true)
		if err != nil {
			return "", err
		}
		if !found {
			return "", errNotExist
		}
		id = childID
	}
	return id, nil
}

// Stat returns the size of key.
func (g *GDrive) Stat(ctx context.Context, key string) (int64, error) {
	if err := keyguard.Validate(key); err != nil {
		return 0, err
	}
	dirs, name := splitKey(key)
	dirID, err := g.resolveDirReadOnly(ctx, dirs)
	if err != nil {
		if errors.Is(err, errNotExist) {
			return 0, fmt.Errorf("%w: %s", errNotExist, key)
		}
		return 0, err
	}

	var size int64
	err = retry.Do(ctx, func(ctx context.Context) error {
		_, sz, found, err := g.api.FindChild(ctx, name, dirID, false)
		if err != nil {
			return err
		}
		if !found {
			return retry.Permanent(fmt.Errorf("%w: %s", errNotExist, key))
		}
		size = sz
		return nil
	})
	if err != nil {
		return 0, err
	}
	return size, nil
}

// List calls fn for every file under prefix, recursing into subfolders.
func (g *GDrive) List(ctx context.Context, prefix string, fn func(key string, size int64) error) error {
	if err := keyguard.Validate(prefix); err != nil {
		return err
	}
	dirs, name := splitKey(prefix)
	if name != "" {
		dirs = append(dirs, name)
	}
	dirID, err := g.resolveDirReadOnly(ctx, dirs)
	if err != nil {
		if errors.Is(err, errNotExist) {
			return nil
		}
		return err
	}
	return g.walk(ctx, dirID, prefix, fn)
}

func (g *GDrive) walk(ctx context.Context, dirID, keyPrefix string, fn func(key string, size int64) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var entries []driveEntry
	err := retry.Do(ctx, func(ctx context.Context) error {
		e, err := g.api.List(ctx, dirID)
		if err != nil {
			return err
		}
		entries = e
		return nil
	})
	if err != nil {
		return err
	}

	for _, e := range entries {
		key := e.Name
		if keyPrefix != "" {
			key = strings.Trim(keyPrefix, "/") + "/" + e.Name
		}
		if e.IsFolder {
			if err := g.walk(ctx, e.ID, key, fn); err != nil {
				return err
			}
			continue
		}
		if err := fn(key, e.Size); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes key. A missing key is not an error.
func (g *GDrive) Delete(ctx context.Context, key string) error {
	if err := keyguard.Validate(key); err != nil {
		return err
	}
	dirs, name := splitKey(key)
	dirID, err := g.resolveDirReadOnly(ctx, dirs)
	if err != nil {
		if errors.Is(err, errNotExist) {
			return nil
		}
		return err
	}

	return retry.Do(ctx, func(ctx context.Context) error {
		id, _, found, err := g.api.FindChild(ctx, name, dirID, false)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		return g.api.Delete(ctx, id)
	})
}

// Close is a no-op for the GDrive backend (the underlying HTTP client owns
// no persistent connections that need explicit closing).
func (g *GDrive) Close() error { return nil }

// bytesReader avoids importing bytes at the top solely for this one call
// site's readability; kept as a tiny local helper.
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

// realDriveAPI adapts the real google.golang.org/api/drive/v3 client to the
// driveAPI interface.
type realDriveAPI struct {
	svc *drive.Service
}

func (a *realDriveAPI) CreateFile(ctx context.Context, name, parentID string, isFolder bool, content io.Reader) (string, error) {
	f := &drive.File{
		Name:    name,
		Parents: []string{parentID},
	}
	if isFolder {
		f.MimeType = folderMimeType
	}
	call := a.svc.Files.Create(f).Context(ctx).Fields("id")
	if content != nil {
		call = call.Media(content)
	}
	out, err := call.Do()
	if err != nil {
		return "", err
	}
	return out.Id, nil
}

func (a *realDriveAPI) FindChild(ctx context.Context, name, parentID string, isFolder bool) (string, int64, bool, error) {
	q := fmt.Sprintf("name = %s and %s in parents and trashed = false", quoteDriveString(name), quoteDriveString(parentID))
	if isFolder {
		q += fmt.Sprintf(" and mimeType = %s", quoteDriveString(folderMimeType))
	} else {
		q += fmt.Sprintf(" and mimeType != %s", quoteDriveString(folderMimeType))
	}
	res, err := a.svc.Files.List().Context(ctx).Q(q).Fields("files(id,name,size,mimeType)").PageSize(1).Do()
	if err != nil {
		return "", 0, false, err
	}
	if len(res.Files) == 0 {
		return "", 0, false, nil
	}
	f := res.Files[0]
	return f.Id, f.Size, true, nil
}

func (a *realDriveAPI) Download(ctx context.Context, id string, offset, length int64) (io.ReadCloser, error) {
	call := a.svc.Files.Get(id).Context(ctx)
	if rng := rangeHeader(offset, length); rng != "" {
		call.Header().Set("Range", rng)
	}
	resp, err := call.Download()
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (a *realDriveAPI) Delete(ctx context.Context, id string) error {
	return a.svc.Files.Delete(id).Context(ctx).Do()
}

func (a *realDriveAPI) List(ctx context.Context, parentID string) ([]driveEntry, error) {
	q := fmt.Sprintf("%s in parents and trashed = false", quoteDriveString(parentID))
	var entries []driveEntry
	pageToken := ""
	for {
		call := a.svc.Files.List().Context(ctx).Q(q).Fields("nextPageToken,files(id,name,size,mimeType)").PageSize(1000)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		res, err := call.Do()
		if err != nil {
			return nil, err
		}
		for _, f := range res.Files {
			entries = append(entries, driveEntry{
				ID:       f.Id,
				Name:     f.Name,
				IsFolder: f.MimeType == folderMimeType,
				Size:     f.Size,
			})
		}
		if res.NextPageToken == "" {
			break
		}
		pageToken = res.NextPageToken
	}
	return entries, nil
}

func quoteDriveString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
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

// isGoogleAPINotFound reports whether err is a googleapi 404 error.
func isGoogleAPINotFound(err error) bool {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return gerr.Code == http.StatusNotFound
	}
	return false
}
