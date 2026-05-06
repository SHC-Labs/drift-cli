package crypto

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ECDH constants. Match the TS relay's ecdh.ts byte-for-byte:
//   - P-256 curve (prime256v1), NOT X25519
//   - 32-byte privkey (left-pad if leading zeroes get stripped)
//   - 65-byte pubkey, uncompressed SEC1 (0x04 || X(32) || Y(32))
const (
	ECDHPrivKeyBytes = 32
	ECDHPubKeyBytes  = 65
)

// ECDHKeyPair is the shape both ends of the wrap exchange share.
// Privkey is the 32-byte scalar; pubkey is the 65-byte uncompressed
// SEC1 point.
type ECDHKeyPair struct {
	Priv []byte
	Pub  []byte
}

// GenerateECDHKeyPair returns a fresh P-256 keypair. Mirrors ecdh.ts
// generateEcdhKeyPair, including the left-pad-to-32-bytes shim for the
// case where Node would have stripped a leading zero byte from the
// scalar.
//
// Go's crypto/ecdh is well-behaved (no zero-stripping), but the field
// width is fixed at 32 bytes by the curve, so we don't need the shim.
// Documenting the parity for future readers comparing TS and Go side
// by side.
func GenerateECDHKeyPair() (*ECDHKeyPair, error) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ecdh: generate p256 key: %w", err)
	}
	privBytes := priv.Bytes()
	pubBytes := priv.PublicKey().Bytes()
	if len(privBytes) != ECDHPrivKeyBytes {
		return nil, fmt.Errorf("ecdh: privkey unexpected length %d", len(privBytes))
	}
	if len(pubBytes) != ECDHPubKeyBytes {
		return nil, fmt.Errorf("ecdh: pubkey unexpected length %d", len(pubBytes))
	}
	return &ECDHKeyPair{Priv: privBytes, Pub: pubBytes}, nil
}

// PubFromPriv returns the pubkey bytes corresponding to a privkey.
// Round-trips via crypto/ecdh.P256().NewPrivateKey() so we get the
// canonical 65-byte uncompressed SEC1 encoding.
func PubFromPriv(privBytes []byte) ([]byte, error) {
	if err := EnsureKeyLen(privBytes, ECDHPrivKeyBytes, "ecdh privkey"); err != nil {
		return nil, err
	}
	priv, err := ecdh.P256().NewPrivateKey(privBytes)
	if err != nil {
		return nil, fmt.Errorf("ecdh: parse privkey: %w", err)
	}
	pub := priv.PublicKey().Bytes()
	if len(pub) != ECDHPubKeyBytes {
		return nil, fmt.Errorf("ecdh: pub length %d unexpected", len(pub))
	}
	return pub, nil
}

// PubKeyFingerprint returns the first 16 hex chars of SHA-256(pubkey).
// Used to identify a recipient pubkey in logs and the keystore. Mirrors
// ecdh.ts pubkeyFingerprint.
func PubKeyFingerprint(pubkey []byte) string {
	sum := sha256.Sum256(pubkey)
	return hex.EncodeToString(sum[:])[:16]
}

// DeriveSharedSecret runs the ECDH handshake for (self.priv, peer.pub)
// and returns the raw shared secret. Both sides computing
// (self.priv, peer.pub) and (peer.priv, self.pub) get the same bytes.
//
// Output is the X coordinate of the shared point, 32 bytes. Caller MUST
// run it through HKDF before using as a symmetric key (we do this
// inside WrapKEKFor / UnwrapKEKFrom).
func DeriveSharedSecret(privBytes, peerPubBytes []byte) ([]byte, error) {
	if err := EnsureKeyLen(privBytes, ECDHPrivKeyBytes, "ecdh privkey"); err != nil {
		return nil, err
	}
	if err := EnsureKeyLen(peerPubBytes, ECDHPubKeyBytes, "ecdh peer pubkey"); err != nil {
		return nil, err
	}
	priv, err := ecdh.P256().NewPrivateKey(privBytes)
	if err != nil {
		return nil, fmt.Errorf("ecdh: parse privkey: %w", err)
	}
	pub, err := ecdh.P256().NewPublicKey(peerPubBytes)
	if err != nil {
		return nil, fmt.Errorf("ecdh: parse pubkey: %w", err)
	}
	secret, err := priv.ECDH(pub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: derive: %w", err)
	}
	return secret, nil
}

// WrappedKEK is the output of WrapKEKFor. Server stores these three
// fields opaquely (base64) and ships them back to the recipient at
// unwrap time.
type WrappedKEK struct {
	Wrapped []byte // ciphertext, 32 bytes for a 32-byte KEK plus 16-byte tag
	Nonce   []byte // 12 bytes
	Tag     []byte // 16 bytes (separated from Wrapped on output to match TS shape)
}

// WrapKEKFor wraps the 32-byte KEK for the recipient identified by their
// pubkey. Symmetric key is HKDF-SHA256(ECDH(self.priv, peer.pub)) with
// the InfoKEKWrap info string. Output mirrors ecdh.ts wrapKekFor:
// separate wrapped_kek / nonce / tag fields.
func WrapKEKFor(kek []byte, senderPriv, recipientPub []byte) (*WrappedKEK, error) {
	if err := EnsureKeyLen(kek, 32, "kek"); err != nil {
		return nil, err
	}
	shared, err := DeriveSharedSecret(senderPriv, recipientPub)
	if err != nil {
		return nil, err
	}
	symKey, err := HKDFSHA256(shared, nil, []byte(InfoKEKWrap), 32)
	if err != nil {
		return nil, err
	}
	aead, err := NewAESGCM256(symKey)
	if err != nil {
		return nil, err
	}
	nonce, err := RandomNonce()
	if err != nil {
		return nil, err
	}
	sealed, err := aead.Seal(kek, nonce, nil)
	if err != nil {
		return nil, err
	}
	// Separate ciphertext from auth tag for the on-wire shape. AES-GCM
	// in Go appends the 16-byte tag to ciphertext; TS keeps them in
	// distinct fields so the server schema can store them separately.
	if len(sealed) < 16 {
		return nil, fmt.Errorf("ecdh: wrap output too short: %d bytes", len(sealed))
	}
	cut := len(sealed) - 16
	return &WrappedKEK{
		Wrapped: sealed[:cut],
		Nonce:   nonce,
		Tag:     sealed[cut:],
	}, nil
}

// UnwrapKEKFrom reverses WrapKEKFor. Symmetric key is the same HKDF
// derivation, just from the recipient's POV. Returns the unwrapped 32-byte
// KEK or an error on tag mismatch / wrong keypair.
func UnwrapKEKFrom(wk *WrappedKEK, recipientPriv, senderPub []byte) ([]byte, error) {
	if len(wk.Nonce) != 12 {
		return nil, fmt.Errorf("ecdh: nonce must be 12 bytes, got %d", len(wk.Nonce))
	}
	if len(wk.Tag) != 16 {
		return nil, fmt.Errorf("ecdh: tag must be 16 bytes, got %d", len(wk.Tag))
	}
	shared, err := DeriveSharedSecret(recipientPriv, senderPub)
	if err != nil {
		return nil, err
	}
	symKey, err := HKDFSHA256(shared, nil, []byte(InfoKEKWrap), 32)
	if err != nil {
		return nil, err
	}
	aead, err := NewAESGCM256(symKey)
	if err != nil {
		return nil, err
	}
	// Re-attach tag for AES-GCM Open.
	cipherWithTag := append(append([]byte{}, wk.Wrapped...), wk.Tag...)
	return aead.Open(cipherWithTag, wk.Nonce, nil)
}
