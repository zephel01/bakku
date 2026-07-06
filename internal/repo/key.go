package repo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/zephel01/bakku/internal/backend"
	"github.com/zephel01/bakku/internal/crypto"
)

// KeyType identifies how a key slot unlocks the master key. Each slot wraps the
// same repository master key with its own KEK, so any one slot can open the
// repository ("lose one key, open with another").
//
// Currently implemented types:
//
//	"password" - argon2id(password) -> KEK -> AES-GCM unwrap master key
//
// The empty string is treated as "password" for backward compatibility with
// v0.1.0 key files, which had no "type" field.
type KeyType string

const (
	// KeyTypePassword is the default password-derived key slot.
	KeyTypePassword KeyType = "password"
)

// keyFile is the on-disk (JSON) representation of a repository key slot. The
// master key is wrapped (AES-GCM) with a KEK. How the KEK is derived depends on
// Type; for "password" it is argon2id(password, Salt, KDFParams).
//
// Backward compatibility: v0.1.0 wrote this struct WITHOUT the "type" field.
// When unmarshaling, a missing/empty Type is interpreted as KeyTypePassword, so
// old repositories open unchanged. New slots always write "type".
//
// Extension point for new key types (e.g. "yubikey-chalresp"): additional
// per-type parameters go in the Params map (json object) so the on-disk schema
// stays stable while new providers add their own fields. See KeyProvider below.
type keyFile struct {
	Version   int              `json:"version"`
	Type      KeyType          `json:"type,omitempty"` // omitempty keeps old-format compatibility on write for "password" if ever needed; new writes always set it
	Created   time.Time        `json:"created"`
	KDF       string           `json:"kdf"` // "argon2id" for password type
	KDFParams crypto.KDFParams `json:"kdf_params"`
	Salt      string           `json:"salt"` // hex, KEK salt
	Data      string           `json:"data"` // hex, wrapped master key (nonce||ct)
	ID        string           `json:"id"`   // hex, this key's id
	// Params carries provider-specific parameters for non-password key types.
	// It is omitted for the "password" type.
	Params map[string]any `json:"params,omitempty"`
}

// effectiveType returns the key type, defaulting a missing type to
// KeyTypePassword for backward compatibility with v0.1.0 key files.
func (kf *keyFile) effectiveType() KeyType {
	if kf.Type == "" {
		return KeyTypePassword
	}
	return kf.Type
}

// KeyProvider knows how to create and unlock a key slot of a particular
// KeyType. This is the registration-based extension point: a subsequent agent
// adding, say, a YubiKey challenge-response slot registers a provider under a
// new type string (e.g. "yubikey-chalresp") without touching the core Open/Init
// unlock loop.
//
// How to add a new key type:
//  1. Implement KeyProvider (Type/CreateSlot/UnlockSlot).
//  2. Call RegisterKeyProvider(myProvider) from an init() in your package.
//  3. Wire a `bakku key add --type <yourtype>` path in the CLI that gathers the
//     provider's credential and calls AddKeySlot.
//
// CreateSlot wraps masterKey for a fresh slot; the returned keyFile is stored
// as-is. UnlockSlot reverses it, returning the master key or ErrWrongPassword
// (or a provider-specific error) when the supplied credential does not match.
//
// The `credential` is provider-defined: for "password" it is the raw password
// bytes; a YubiKey provider might interpret it as a serialized challenge or an
// unused placeholder (deriving the secret from hardware instead).
type KeyProvider interface {
	// Type returns the KeyType this provider handles.
	Type() KeyType
	// CreateSlot builds a new key slot wrapping masterKey using credential.
	CreateSlot(credential, masterKey []byte) (keyFile, error)
	// UnlockSlot attempts to unwrap the master key from kf using credential.
	UnlockSlot(kf keyFile, credential []byte) (masterKey []byte, err error)
}

// keyProviders is the registry of KeyProviders keyed by KeyType. It is
// populated at init time; see RegisterKeyProvider.
var keyProviders = map[KeyType]KeyProvider{}

// RegisterKeyProvider registers p under p.Type(). It panics on duplicate
// registration (a programming error). Call from an init() function.
func RegisterKeyProvider(p KeyProvider) {
	t := p.Type()
	if _, dup := keyProviders[t]; dup {
		panic(fmt.Sprintf("repo: duplicate key provider for type %q", t))
	}
	keyProviders[t] = p
}

// providerFor returns the registered provider for t, or an error if none.
func providerFor(t KeyType) (KeyProvider, error) {
	p, ok := keyProviders[t]
	if !ok {
		return nil, fmt.Errorf("repo: unknown key type %q (no provider registered)", t)
	}
	return p, nil
}

func init() {
	RegisterKeyProvider(passwordProvider{})
}

// passwordProvider implements the built-in "password" key type: argon2id KEK
// derivation plus AES-GCM wrapping of the master key.
type passwordProvider struct{}

func (passwordProvider) Type() KeyType { return KeyTypePassword }

func (passwordProvider) CreateSlot(password, masterKey []byte) (keyFile, error) {
	salt, err := crypto.NewSalt()
	if err != nil {
		return keyFile{}, err
	}
	params := crypto.DefaultKDFParams()
	kek := crypto.DeriveKEK(password, salt, params)
	wrapped, err := crypto.Seal(kek, masterKey, nil)
	crypto.Wipe(kek) // KEK no longer needed once the master key is wrapped
	if err != nil {
		return keyFile{}, err
	}
	id, err := randomHex(16)
	if err != nil {
		return keyFile{}, err
	}
	return keyFile{
		Version:   repoVersion,
		Type:      KeyTypePassword,
		Created:   time.Now().UTC(),
		KDF:       "argon2id",
		KDFParams: params,
		Salt:      hex.EncodeToString(salt),
		Data:      hex.EncodeToString(wrapped),
		ID:        id,
	}, nil
}

func (passwordProvider) UnlockSlot(kf keyFile, password []byte) ([]byte, error) {
	salt, err := hex.DecodeString(kf.Salt)
	if err != nil {
		return nil, fmt.Errorf("repo: corrupt key salt: %w", err)
	}
	wrapped, err := hex.DecodeString(kf.Data)
	if err != nil {
		return nil, fmt.Errorf("repo: corrupt key data: %w", err)
	}
	kek := crypto.DeriveKEK(password, salt, kf.KDFParams)
	mk, err := crypto.Open(kek, wrapped, nil)
	crypto.Wipe(kek) // KEK no longer needed once the master key is unwrapped
	if err != nil {
		return nil, ErrWrongPassword
	}
	if len(mk) != crypto.KeySize {
		return nil, errors.New("repo: unwrapped master key has wrong size")
	}
	return mk, nil
}

// ErrWrongPassword is returned when no key slot can be unlocked with the
// supplied credential.
var ErrWrongPassword = errors.New("repo: wrong password or no matching key")

// ErrLastKeySlot is returned when removing a key slot would leave the
// repository with no way to unlock it.
var ErrLastKeySlot = errors.New("repo: cannot remove the last remaining key slot")

// createKeyFile wraps masterKey with a password-derived KEK and returns the
// serialized key file plus its id. Retained for Init and password-based paths.
func createKeyFile(password, masterKey []byte) (id string, blob []byte, err error) {
	kf, err := passwordProvider{}.CreateSlot(password, masterKey)
	if err != nil {
		return "", nil, err
	}
	blob, err = json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return "", nil, err
	}
	return kf.ID, blob, nil
}

// unwrapKeyFile parses a key file blob and unwraps the master key using the
// registered provider for the slot's type and the given credential. Returns
// ErrWrongPassword when the credential does not match.
func unwrapKeyFile(blob, credential []byte) (masterKey []byte, err error) {
	kf, err := parseKeyFile(blob)
	if err != nil {
		return nil, err
	}
	p, err := providerFor(kf.effectiveType())
	if err != nil {
		return nil, err
	}
	return p.UnlockSlot(kf, credential)
}

// parseKeyFile unmarshals a key file blob, applying the backward-compat default
// for the type field.
func parseKeyFile(blob []byte) (keyFile, error) {
	var kf keyFile
	if err := json.Unmarshal(blob, &kf); err != nil {
		return keyFile{}, fmt.Errorf("repo: corrupt key file: %w", err)
	}
	kf.Type = kf.effectiveType()
	return kf, nil
}

// loadMasterKey iterates keys/ in the backend and returns the first master key
// that unlocks with credential, along with the id of the slot that unlocked it.
func loadMasterKey(ctx context.Context, be backend.Backend, credential []byte) (masterKey []byte, keyID string, err error) {
	var found []byte
	var foundID string
	err = be.List(ctx, "keys", func(key string, _ int64) error {
		if found != nil {
			return nil
		}
		blob, rerr := readAll(ctx, be, key)
		if rerr != nil {
			return rerr
		}
		kf, perr := parseKeyFile(blob)
		if perr != nil {
			return perr
		}
		p, perr := providerFor(kf.effectiveType())
		if perr != nil {
			// Unknown key type: skip this slot rather than failing the whole
			// open (another slot may still unlock the repo).
			return nil
		}
		mk, uerr := p.UnlockSlot(kf, credential)
		if uerr == nil {
			found = mk
			foundID = kf.ID
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if found == nil {
		return nil, "", ErrWrongPassword
	}
	return found, foundID, nil
}

// KeySlotInfo describes a key slot for `bakku key list`.
type KeySlotInfo struct {
	ID      string    `json:"id"`
	Type    KeyType   `json:"type"`
	Created time.Time `json:"created"`
	// Current reports whether this slot was the one used to open the repo.
	Current bool `json:"current"`
}

// loadedSlot bundles a parsed key file with its backend key path.
type loadedSlot struct {
	backendKey string
	kf         keyFile
}

// listKeySlots enumerates all key slots in keys/, sorted by creation time then
// ID for stable output.
func listKeySlots(ctx context.Context, be backend.Backend) ([]loadedSlot, error) {
	var slots []loadedSlot
	err := be.List(ctx, "keys", func(key string, _ int64) error {
		blob, err := readAll(ctx, be, key)
		if err != nil {
			return err
		}
		kf, err := parseKeyFile(blob)
		if err != nil {
			return err
		}
		slots = append(slots, loadedSlot{backendKey: key, kf: kf})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(slots, func(i, j int) bool {
		if !slots[i].kf.Created.Equal(slots[j].kf.Created) {
			return slots[i].kf.Created.Before(slots[j].kf.Created)
		}
		return slots[i].kf.ID < slots[j].kf.ID
	})
	return slots, nil
}

// findSlotByID resolves a possibly-abbreviated key id (>=1 hex char) to a full
// slot, erroring on ambiguity or no match.
func findSlotByID(slots []loadedSlot, prefix string) (loadedSlot, error) {
	var match *loadedSlot
	for i := range slots {
		if hasPrefix(slots[i].kf.ID, prefix) {
			if match != nil {
				return loadedSlot{}, fmt.Errorf("repo: key id %q is ambiguous", prefix)
			}
			match = &slots[i]
		}
	}
	if match == nil {
		return loadedSlot{}, fmt.Errorf("repo: no key slot matching %q", prefix)
	}
	return *match, nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// readAll loads an entire key from the backend into memory.
func readAll(ctx context.Context, be backend.Backend, key string) ([]byte, error) {
	rc, err := be.Load(ctx, key, 0, -1)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
