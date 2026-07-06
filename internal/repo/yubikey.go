package repo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/zephel01/bakku/internal/backend"
	"github.com/zephel01/bakku/internal/crypto"
	"github.com/zephel01/bakku/internal/yubikey"
)

// randomBytesN returns n cryptographically random bytes.
func randomBytesN(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

// KeyTypeYubiKey is the YubiKey HMAC-SHA1 challenge-response key type. See
// docs/quickguide.md ("YubiKeyでパスワードレス開錠") for the end-user setup guide
// and the internal/yubikey package doc comment for the wire protocol.
const KeyTypeYubiKey KeyType = "yubikey-chalresp"

// yubiKeyKDFContext is the fixed BLAKE3 derive-key context used to stretch a
// YubiKey HMAC-SHA1 response into a 32-byte KEK. This is a distinct context
// from the repository's data/index/snapshot subkeys (see ctxDataKey et al. in
// repository.go) and is never reused for another purpose.
const yubiKeyKDFContext = "github.com/zephel01/bakku 2026 yubikey KEK"

// defaultYubiKeySlot is the OTP slot bakku targets when the user does not
// specify one. Slot 2 is the conventional "long touch" challenge-response
// slot; slot 1 is often left as the factory Yubico OTP credential.
const defaultYubiKeySlot = 2

func init() {
	RegisterKeyProvider(yubiKeyProvider{})
}

// yubiKeyProvider implements the "yubikey-chalresp" KeyType for the
// KeyProvider registry (used by the automatic UnlockSlot loop in Open/
// loadMasterKey). Its CreateSlot/UnlockSlot honor the KeyProvider interface,
// but because creating/unlocking a YubiKey slot requires talking to hardware
// (a Responder), the CLI-facing entry points are the package-level
// AddYubiKeySlot/unlockYubiKeySlot helpers below, not this type directly. This
// mirrors passwordProvider's relationship to createKeyFile.
type yubiKeyProvider struct{}

func (yubiKeyProvider) Type() KeyType { return KeyTypeYubiKey }

// CreateSlot is not used directly by the CLI (see AddYubiKeySlot) but exists
// to satisfy KeyProvider. credential must be a JSON-encoded yubiKeyCredential
// produced by exchangeYubiKeyChallenge; this indirection exists purely so the
// type satisfies the generic (credential, masterKey []byte) signature.
func (yubiKeyProvider) CreateSlot(credential, masterKey []byte) (keyFile, error) {
	var cred yubiKeyCredential
	if err := json.Unmarshal(credential, &cred); err != nil {
		return keyFile{}, fmt.Errorf("repo: invalid yubikey credential: %w", err)
	}
	return buildYubiKeySlot(cred, masterKey)
}

// UnlockSlot re-derives the KEK from Params.challenge (the provider must
// already have re-collected the matching response into `credential`, which is
// the raw 20-byte HMAC-SHA1 response). This is what the automatic Open/
// loadMasterKey loop calls once a Responder has produced a response; see
// unlockYubiKeySlotWithResponse for the caller that supplies `credential`.
func (yubiKeyProvider) UnlockSlot(kf keyFile, credential []byte) ([]byte, error) {
	if len(credential) != yubikey.ResponseSize {
		// The generic Open() unlock loop tries every provider with the same
		// password-shaped credential (e.g. a text password); a YubiKey slot
		// simply never matches that shape. Returning ErrWrongPassword lets the
		// loop continue trying other slots instead of failing outright.
		return nil, ErrWrongPassword
	}
	salt, err := hex.DecodeString(kf.Salt)
	if err != nil {
		return nil, fmt.Errorf("repo: corrupt key salt: %w", err)
	}
	wrapped, err := hex.DecodeString(kf.Data)
	if err != nil {
		return nil, fmt.Errorf("repo: corrupt key data: %w", err)
	}
	kek := deriveYubiKEKForSlot(kf, credential, salt)
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

// yubiKeyCredential is the intermediate value passed to
// yubiKeyProvider.CreateSlot via the generic KeyProvider.CreateSlot(credential
// []byte, ...) signature.
type yubiKeyCredential struct {
	Slot      int    `json:"slot"`
	Challenge []byte `json:"challenge"` // raw bytes; json.Marshal base64-encodes
	Response  []byte `json:"response"`  // the 20-byte HMAC-SHA1 response (secret material, never persisted)
}

// yubiKeyKDFv2 is the value stored in a slot's Params["kdf"] to mark it as
// using HKDF-SHA256 derivation (deriveYubiKEKHKDF). Slots written before
// v0.2.4 have no such marker and use the legacy deriveYubiKEKLegacy path, so
// existing YubiKey slots keep unlocking unchanged (backward compatible).
const yubiKeyKDFv2 = "hkdf-sha256"

// deriveYubiKEKHKDF stretches a YubiKey HMAC-SHA1 response into a 32-byte KEK
// using HKDF-SHA256 with the per-slot random salt as the HKDF salt and the
// fixed application context as the HKDF info. This is the standard
// extract-then-expand construction for a high-entropy secret and uses the salt
// correctly (as an independent input rather than concatenated key material).
// Used for all slots created from v0.2.4 onward.
func deriveYubiKEKHKDF(response, salt []byte) []byte {
	kek := make([]byte, crypto.KeySize)
	r := hkdf.New(sha256.New, response, salt, []byte(yubiKeyKDFContext))
	if _, err := io.ReadFull(r, kek); err != nil {
		// HKDF over a fixed 32-byte output never fails in practice; treat any
		// failure as fatal misuse rather than returning a partial key.
		panic("repo: HKDF KEK derivation failed: " + err.Error())
	}
	return kek
}

// deriveYubiKEKLegacy is the pre-v0.2.4 derivation: BLAKE3 derive-key over
// response||salt. Retained so slots created by older bakku versions still
// unlock. New slots use deriveYubiKEKHKDF instead.
func deriveYubiKEKLegacy(response, salt []byte) []byte {
	material := append(append([]byte{}, response...), salt...)
	kek := crypto.DeriveSubKey(material, yubiKeyKDFContext)
	crypto.Wipe(material)
	return kek
}

// deriveYubiKEKForSlot picks the derivation matching how the slot was written:
// HKDF for slots marked yubiKeyKDFv2 in Params["kdf"], legacy BLAKE3 otherwise.
func deriveYubiKEKForSlot(kf keyFile, response, salt []byte) []byte {
	if kdf, _ := kf.Params["kdf"].(string); kdf == yubiKeyKDFv2 {
		return deriveYubiKEKHKDF(response, salt)
	}
	return deriveYubiKEKLegacy(response, salt)
}

// buildYubiKeySlot constructs the keyFile for a fresh YubiKey slot: a random
// salt, KEK derivation from cred.Response, and AES-GCM wrap of masterKey. The
// challenge and slot number are stored in Params in the clear (see the
// package doc in internal/yubikey: the challenge is not secret).
func buildYubiKeySlot(cred yubiKeyCredential, masterKey []byte) (keyFile, error) {
	if len(cred.Response) != yubikey.ResponseSize {
		return keyFile{}, fmt.Errorf("repo: yubikey response must be %d bytes, got %d", yubikey.ResponseSize, len(cred.Response))
	}
	salt, err := crypto.NewSalt()
	if err != nil {
		return keyFile{}, err
	}
	kek := deriveYubiKEKHKDF(cred.Response, salt)
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
		Version: repoVersion,
		Type:    KeyTypeYubiKey,
		Created: time.Now().UTC(),
		Salt:    hex.EncodeToString(salt),
		Data:    hex.EncodeToString(wrapped),
		ID:      id,
		Params: map[string]any{
			"slot":      cred.Slot,
			"challenge": hex.EncodeToString(cred.Challenge),
			"kdf":       yubiKeyKDFv2,
		},
	}, nil
}

// slotParams extracts the OTP slot number and hex challenge stored in a
// yubikey-chalresp key file's Params.
func slotParams(kf keyFile) (slot int, challenge []byte, err error) {
	rawSlot, ok := kf.Params["slot"]
	if !ok {
		return 0, nil, errors.New("repo: yubikey key file missing \"slot\" param")
	}
	// encoding/json unmarshals numbers as float64 into map[string]any.
	switch v := rawSlot.(type) {
	case float64:
		slot = int(v)
	case int:
		slot = v
	default:
		return 0, nil, fmt.Errorf("repo: yubikey key file has non-numeric \"slot\" param (%T)", rawSlot)
	}
	rawChallenge, ok := kf.Params["challenge"]
	if !ok {
		return 0, nil, errors.New("repo: yubikey key file missing \"challenge\" param")
	}
	hexChallenge, ok := rawChallenge.(string)
	if !ok {
		return 0, nil, errors.New("repo: yubikey key file has non-string \"challenge\" param")
	}
	challenge, err = hex.DecodeString(hexChallenge)
	if err != nil {
		return 0, nil, fmt.Errorf("repo: corrupt yubikey challenge: %w", err)
	}
	return slot, challenge, nil
}

// exchangeYubiKeyChallenge sends challenge to the YubiKey (via r) on slot and
// returns the 20-byte response, translating context cancellation/timeouts
// into a caller-friendly error.
func exchangeYubiKeyChallenge(ctx context.Context, r yubikey.Responder, slot int, challenge []byte) ([]byte, error) {
	resp, err := r.Respond(ctx, slot, challenge)
	if err != nil {
		return nil, fmt.Errorf("repo: yubikey challenge-response failed: %w", err)
	}
	out := make([]byte, len(resp))
	copy(out, resp[:])
	return out, nil
}

// AddYubiKeySlot adds a new YubiKey challenge-response key slot to an already
// open repository, using r to communicate with the (real or mock) YubiKey on
// the given OTP slot. It generates a fresh random challenge, exchanges it
// twice to confirm the YubiKey gives a stable response before committing
// anything to the backend (protects against a flaky/misconfigured slot
// silently locking the user out), derives a KEK, wraps the repository master
// key, and stores the new slot. Returns the new slot id.
func (r *Repository) AddYubiKeySlot(ctx context.Context, responder yubikey.Responder, slot int) (string, error) {
	if slot <= 0 {
		slot = defaultYubiKeySlot
	}
	challenge, err := randomBytesN(yubikey.ChallengeSize)
	if err != nil {
		return "", err
	}

	resp1, err := exchangeYubiKeyChallenge(ctx, responder, slot, challenge)
	if err != nil {
		return "", err
	}
	// Confirm stability: a second exchange with the same challenge must give
	// the exact same response, or the slot is unusable (misconfigured,
	// wrong-slot HOTP-only mode, etc.) and we must not brick the repository on
	// a bad key file.
	resp2, err := exchangeYubiKeyChallenge(ctx, responder, slot, challenge)
	if err != nil {
		return "", err
	}
	if !bytesEqual(resp1, resp2) {
		return "", errors.New("repo: yubikey gave two different responses to the same challenge; slot may not be configured for HMAC-SHA1 challenge-response (see docs/quickguide.md)")
	}

	kf, err := buildYubiKeySlot(yubiKeyCredential{Slot: slot, Challenge: challenge, Response: resp1}, r.master)
	if err != nil {
		return "", err
	}
	blob, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return "", err
	}
	if err := r.be.Save(ctx, "keys/"+kf.ID, bytesReader(blob), int64(len(blob))); err != nil {
		return "", err
	}
	return kf.ID, nil
}

// OpenWithYubiKey opens a repository (be) whose master key is protected (at
// least in part) by a yubikey-chalresp slot, using responder to answer
// whichever challenges are stored in the repository's key slots. It tries
// every yubikey-chalresp slot found in keys/ (oldest first) and returns the
// first one that unlocks, mirroring the password-based Open's "try every
// slot" semantics. On success it loads the index exactly like Open, returning
// a ready-to-use *Repository.
func OpenWithYubiKey(ctx context.Context, be backend.Backend, responder yubikey.Responder) (*Repository, error) {
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
	master, keyID, err := unlockWithYubiKey(ctx, be, responder)
	if err != nil {
		return nil, err
	}
	r := newRepository(be, cfg, master)
	r.openedKeyID = keyID
	ix, err := loadIndex(ctx, be, r.indexKey)
	if err != nil {
		return nil, err
	}
	r.index = ix
	return r, nil
}

// unlockWithYubiKey tries every yubikey-chalresp slot in be's keys/ directory,
// returning the master key and slot id of the first one that unlocks.
func unlockWithYubiKey(ctx context.Context, be backend.Backend, responder yubikey.Responder) (masterKey []byte, keyID string, err error) {
	slots, err := listKeySlots(ctx, be)
	if err != nil {
		return nil, "", err
	}
	for _, s := range slots {
		if s.kf.effectiveType() != KeyTypeYubiKey {
			continue
		}
		slot, challenge, perr := slotParams(s.kf)
		if perr != nil {
			continue
		}
		resp, rerr := exchangeYubiKeyChallenge(ctx, responder, slot, challenge)
		if rerr != nil {
			// A single unreachable/wrong YubiKey should not abort trying other
			// slots (e.g. the user has two YubiKeys registered and plugged in
			// the backup one).
			continue
		}
		mk, uerr := yubiKeyProvider{}.UnlockSlot(s.kf, resp)
		if uerr == nil {
			return mk, s.kf.ID, nil
		}
	}
	return nil, "", ErrWrongPassword
}

// HasYubiKeySlot reports whether the repository at be has at least one
// yubikey-chalresp key slot. Exposed for the CLI's auto-fallback decision
// (only attempt YubiKey unlock if a slot exists AND a tool is on PATH).
func HasYubiKeySlot(ctx context.Context, be backend.Backend) (bool, error) {
	slots, err := listKeySlots(ctx, be)
	if err != nil {
		return false, err
	}
	for _, s := range slots {
		if s.kf.effectiveType() == KeyTypeYubiKey {
			return true, nil
		}
	}
	return false, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
