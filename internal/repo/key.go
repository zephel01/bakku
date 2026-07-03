package repo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/zephel01/bakku/internal/backend"
	"github.com/zephel01/bakku/internal/crypto"
)

// keyFile is the on-disk (JSON) representation of a repository key. The master
// key is wrapped (AES-GCM) with a KEK derived from the password via argon2id.
type keyFile struct {
	Version   int              `json:"version"`
	Created   time.Time        `json:"created"`
	KDF       string           `json:"kdf"` // "argon2id"
	KDFParams crypto.KDFParams `json:"kdf_params"`
	Salt      string           `json:"salt"`       // hex, KEK salt
	Data      string           `json:"data"`       // hex, wrapped master key (nonce||ct)
	ID        string           `json:"id"`         // hex, this key's id
}

// createKeyFile wraps masterKey with a password-derived KEK and returns the
// serialized key file plus its id.
func createKeyFile(password, masterKey []byte) (id string, blob []byte, err error) {
	salt, err := crypto.NewSalt()
	if err != nil {
		return "", nil, err
	}
	params := crypto.DefaultKDFParams()
	kek := crypto.DeriveKEK(password, salt, params)
	wrapped, err := crypto.Seal(kek, masterKey, nil)
	if err != nil {
		return "", nil, err
	}
	idBytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, idBytes); err != nil {
		return "", nil, err
	}
	id = hex.EncodeToString(idBytes)
	kf := keyFile{
		Version:   repoVersion,
		Created:   time.Now().UTC(),
		KDF:       "argon2id",
		KDFParams: params,
		Salt:      hex.EncodeToString(salt),
		Data:      hex.EncodeToString(wrapped),
		ID:        id,
	}
	blob, err = json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return "", nil, err
	}
	return id, blob, nil
}

// unwrapKeyFile parses a key file blob and unwraps the master key with the
// password. Returns ErrWrongPassword if the KEK does not decrypt the key.
func unwrapKeyFile(blob, password []byte) (masterKey []byte, err error) {
	var kf keyFile
	if err := json.Unmarshal(blob, &kf); err != nil {
		return nil, fmt.Errorf("repo: corrupt key file: %w", err)
	}
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
	if err != nil {
		return nil, ErrWrongPassword
	}
	if len(mk) != crypto.KeySize {
		return nil, errors.New("repo: unwrapped master key has wrong size")
	}
	return mk, nil
}

// ErrWrongPassword is returned when no key file can be unlocked with the
// supplied password.
var ErrWrongPassword = errors.New("repo: wrong password or no matching key")

// loadMasterKey iterates keys/ in the backend and returns the first master key
// that unlocks with password.
func loadMasterKey(ctx context.Context, be backend.Backend, password []byte) ([]byte, error) {
	var found []byte
	err := be.List(ctx, "keys", func(key string, _ int64) error {
		if found != nil {
			return nil
		}
		blob, err := readAll(ctx, be, key)
		if err != nil {
			return err
		}
		mk, err := unwrapKeyFile(blob, password)
		if err == nil {
			found = mk
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, ErrWrongPassword
	}
	return found, nil
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
