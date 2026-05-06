package crypto

import (
	"bytes"
	"testing"
	"time"
)

func TestECDHRoundTrip(t *testing.T) {
	alice, err := GenerateECDHKeyPair()
	if err != nil {
		t.Fatalf("alice keygen: %v", err)
	}
	bob, err := GenerateECDHKeyPair()
	if err != nil {
		t.Fatalf("bob keygen: %v", err)
	}
	if len(alice.Priv) != ECDHPrivKeyBytes {
		t.Errorf("alice privkey len = %d, want %d", len(alice.Priv), ECDHPrivKeyBytes)
	}
	if len(alice.Pub) != ECDHPubKeyBytes {
		t.Errorf("alice pubkey len = %d, want %d", len(alice.Pub), ECDHPubKeyBytes)
	}
	if alice.Pub[0] != 0x04 {
		t.Errorf("alice pubkey not uncompressed SEC1: first byte = 0x%02x, want 0x04", alice.Pub[0])
	}

	// ECDH symmetry: (alice.priv, bob.pub) == (bob.priv, alice.pub)
	secretA, err := DeriveSharedSecret(alice.Priv, bob.Pub)
	if err != nil {
		t.Fatalf("derive shared (alice, bob): %v", err)
	}
	secretB, err := DeriveSharedSecret(bob.Priv, alice.Pub)
	if err != nil {
		t.Fatalf("derive shared (bob, alice): %v", err)
	}
	if !bytes.Equal(secretA, secretB) {
		t.Errorf("shared secrets differ: alice=%x, bob=%x", secretA, secretB)
	}
}

func TestKEKWrapRoundTrip(t *testing.T) {
	sender, _ := GenerateECDHKeyPair()
	recipient, _ := GenerateECDHKeyPair()
	kek := bytes.Repeat([]byte{0x77}, 32)

	wrapped, err := WrapKEKFor(kek, sender.Priv, recipient.Pub)
	if err != nil {
		t.Fatalf("WrapKEKFor: %v", err)
	}
	if len(wrapped.Nonce) != 12 {
		t.Errorf("wrapped nonce len = %d, want 12", len(wrapped.Nonce))
	}
	if len(wrapped.Tag) != 16 {
		t.Errorf("wrapped tag len = %d, want 16", len(wrapped.Tag))
	}
	if len(wrapped.Wrapped) != 32 {
		t.Errorf("wrapped ciphertext len = %d, want 32 (32-byte KEK)", len(wrapped.Wrapped))
	}

	unwrapped, err := UnwrapKEKFrom(wrapped, recipient.Priv, sender.Pub)
	if err != nil {
		t.Fatalf("UnwrapKEKFrom: %v", err)
	}
	if !bytes.Equal(unwrapped, kek) {
		t.Errorf("unwrapped KEK differs from original")
	}
}

func TestPubKeyFingerprintStable(t *testing.T) {
	pub := bytes.Repeat([]byte{0x01}, ECDHPubKeyBytes)
	pub[0] = 0x04 // uncompressed marker
	fp1 := PubKeyFingerprint(pub)
	fp2 := PubKeyFingerprint(pub)
	if fp1 != fp2 {
		t.Errorf("fingerprint not stable: %s vs %s", fp1, fp2)
	}
	if len(fp1) != 16 {
		t.Errorf("fingerprint len = %d, want 16 hex chars", len(fp1))
	}
}

func TestDeriveSessionKeyDeterministic(t *testing.T) {
	orgKey := bytes.Repeat([]byte{0x33}, 32)
	day := time.Date(2026, 5, 5, 23, 0, 0, 0, time.UTC)
	k1, err := DeriveSessionKey(orgKey, day)
	if err != nil {
		t.Fatalf("DeriveSessionKey: %v", err)
	}
	k2, err := DeriveSessionKey(orgKey, day)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Errorf("session key not deterministic on same day")
	}
	if len(k1) != SessionKeyBytes {
		t.Errorf("session key len = %d, want %d", len(k1), SessionKeyBytes)
	}

	// Different day should produce different key.
	nextDay := day.Add(24 * time.Hour)
	k3, _ := DeriveSessionKey(orgKey, nextDay)
	if bytes.Equal(k1, k3) {
		t.Errorf("session keys for different days are equal (HKDF info collision)")
	}
}

func TestFingerprintMatchesTSFormat(t *testing.T) {
	// 4-byte HMAC-SHA256 truncated, hex-encoded = 8 hex chars.
	orgKey := bytes.Repeat([]byte{0x42}, 32)
	fp := Fingerprint(orgKey)
	if len(fp) != 8 {
		t.Errorf("fingerprint len = %d, want 8 hex chars (4 bytes)", len(fp))
	}
	// Should be deterministic.
	fp2 := Fingerprint(orgKey)
	if fp != fp2 {
		t.Errorf("fingerprint not deterministic")
	}
}
