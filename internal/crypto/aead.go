package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// AEAD is the interface every authenticated encryption algorithm in
// drift implements. v1 ships AES-GCM-256 only; ChaCha20-Poly1305 is
// reserved for v1.x via the negotiation handshake. The interface lets
// the relay code pick an algorithm at handshake time without per-algo
// branching.
//
// Mirrors crypto/cipher.AEAD's shape (Seal / Open) plus identity
// metadata so the negotiation handshake can serialize the choice.
type AEAD interface {
	// Seal encrypts plaintext with key + nonce, appending the auth tag
	// to the ciphertext. additionalData is authenticated but not
	// encrypted (associated data, ignored in v1, kept for v2+).
	Seal(plaintext, nonce, additionalData []byte) ([]byte, error)

	// Open decrypts ciphertext with key + nonce, verifying the auth tag
	// and additionalData. Returns the plaintext or an error if the tag
	// fails (wrong key or corrupted ciphertext).
	Open(ciphertext, nonce, additionalData []byte) ([]byte, error)

	// NonceSize returns the algorithm's nonce length in bytes.
	NonceSize() int

	// AlgorithmID returns the wire-format identifier the negotiation
	// handshake uses (e.g. "aes-gcm-256", "chacha20-poly1305").
	AlgorithmID() string
}

// AESGCM256 wraps Go's crypto/cipher AES-GCM with a 32-byte key. The
// algorithm v1 ships, byte-identical to the TS relay's
// crypto.createCipheriv('aes-256-gcm', key, nonce).
type AESGCM256 struct {
	gcm cipher.AEAD
}

// NewAESGCM256 builds an AESGCM256 from the 32-byte key. Returns an
// error if the key length is wrong; everything else (nonce length, tag
// shape) is fixed by the algorithm and validated at Seal/Open time.
func NewAESGCM256(key []byte) (*AESGCM256, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("aes-gcm-256: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm-256: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm-256: new gcm: %w", err)
	}
	return &AESGCM256{gcm: gcm}, nil
}

// Seal encrypts plaintext + auth-tags. Output is ciphertext || tag.
// nonce must be exactly 12 bytes; additionalData may be empty.
func (a *AESGCM256) Seal(plaintext, nonce, additionalData []byte) ([]byte, error) {
	if len(nonce) != a.gcm.NonceSize() {
		return nil, fmt.Errorf("aes-gcm-256: nonce must be %d bytes, got %d", a.gcm.NonceSize(), len(nonce))
	}
	return a.gcm.Seal(nil, nonce, plaintext, additionalData), nil
}

// Open decrypts + verifies. Input is ciphertext || tag (the same shape
// Seal output). Returns ErrAuthFailed on any verification failure.
func (a *AESGCM256) Open(ciphertext, nonce, additionalData []byte) ([]byte, error) {
	if len(nonce) != a.gcm.NonceSize() {
		return nil, fmt.Errorf("aes-gcm-256: nonce must be %d bytes, got %d", a.gcm.NonceSize(), len(nonce))
	}
	out, err := a.gcm.Open(nil, nonce, ciphertext, additionalData)
	if err != nil {
		return nil, ErrAuthFailed
	}
	return out, nil
}

// NonceSize is 12 bytes (96 bits) for AES-GCM, the NIST SP 800-38D
// recommended size. Matches the TS relay's NONCE_BYTES.
func (a *AESGCM256) NonceSize() int { return a.gcm.NonceSize() }

// AlgorithmID is the wire identifier the negotiation handshake uses.
func (a *AESGCM256) AlgorithmID() string { return "aes-gcm-256" }

// RandomNonce returns a 12-byte random nonce. Match the TS relay's
// randomBytes(NONCE_BYTES) call site for byte-identical compatibility.
//
// Per THREAT_MODEL.md, NIST SP 800-38D headroom is ~2^32 invocations
// per key with random nonces. DEKs rotate on demand to stay well below.
func RandomNonce() ([]byte, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("read entropy: %w", err)
	}
	return nonce, nil
}

// ErrAuthFailed is the loss-of-context error every AEAD returns on
// authentication failure. Don't leak whether the key was wrong vs the
// ciphertext was corrupted; both are "tag mismatch" to the caller.
var ErrAuthFailed = errors.New("authenticated decryption failed (wrong key or corrupted ciphertext)")
