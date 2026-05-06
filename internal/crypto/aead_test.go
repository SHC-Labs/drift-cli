package crypto

import (
	"bytes"
	"strings"
	"testing"
)

func TestAESGCM256RoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	a, err := NewAESGCM256(key)
	if err != nil {
		t.Fatalf("NewAESGCM256: %v", err)
	}
	if a.AlgorithmID() != "aes-gcm-256" {
		t.Errorf("AlgorithmID = %q, want %q", a.AlgorithmID(), "aes-gcm-256")
	}
	if a.NonceSize() != 12 {
		t.Errorf("NonceSize = %d, want 12", a.NonceSize())
	}

	plaintext := []byte("the quick brown fox jumps over the lazy dog")
	nonce := bytes.Repeat([]byte{0x01}, 12)
	sealed, err := a.Seal(plaintext, nonce, nil)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := a.Open(sealed, nonce, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("Open returned %q, want %q", got, plaintext)
	}
}

func TestAESGCM256WrongKeyFailsAuth(t *testing.T) {
	keyA := bytes.Repeat([]byte{0x01}, 32)
	keyB := bytes.Repeat([]byte{0x02}, 32)
	a, _ := NewAESGCM256(keyA)
	b, _ := NewAESGCM256(keyB)
	nonce := bytes.Repeat([]byte{0xff}, 12)
	sealed, _ := a.Seal([]byte("secret"), nonce, nil)
	if _, err := b.Open(sealed, nonce, nil); err != ErrAuthFailed {
		t.Errorf("Open with wrong key: err = %v, want ErrAuthFailed", err)
	}
}

func TestAESGCM256TamperFailsAuth(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	a, _ := NewAESGCM256(key)
	nonce := bytes.Repeat([]byte{0x01}, 12)
	sealed, _ := a.Seal([]byte("secret"), nonce, nil)
	sealed[0] ^= 0x80 // flip a bit in the ciphertext
	if _, err := a.Open(sealed, nonce, nil); err != ErrAuthFailed {
		t.Errorf("Open with tampered ciphertext: err = %v, want ErrAuthFailed", err)
	}
}

func TestEncryptContentRoundTrip(t *testing.T) {
	dek := bytes.Repeat([]byte{0x99}, 32)
	plaintext := "drift e2ee round trip test message"

	blob, err := EncryptContent(plaintext, dek, 1, "")
	if err != nil {
		t.Fatalf("EncryptContent: %v", err)
	}
	if !strings.HasPrefix(blob, EnvelopePrefix) {
		t.Errorf("blob missing prefix %q: %s", EnvelopePrefix, blob[:30])
	}
	if !IsCiphertext(blob) {
		t.Errorf("IsCiphertext returned false for valid blob")
	}

	got, err := DecryptContent(blob, dek)
	if err != nil {
		t.Fatalf("DecryptContent: %v", err)
	}
	if got != plaintext {
		t.Errorf("DecryptContent = %q, want %q", got, plaintext)
	}
}

func TestEncryptContentWithProjectHash(t *testing.T) {
	dek := bytes.Repeat([]byte{0x55}, 32)
	plaintext := "per-project encrypted content"
	projHash := "abcdef1234567890"

	blob, err := EncryptContent(plaintext, dek, 2, projHash)
	if err != nil {
		t.Fatalf("EncryptContent: %v", err)
	}

	meta := InspectEnvelope(blob)
	if meta == nil {
		t.Fatal("InspectEnvelope returned nil")
	}
	if meta.DEKVersion != 2 {
		t.Errorf("dek_version = %d, want 2", meta.DEKVersion)
	}
	if meta.ProjectHash != projHash {
		t.Errorf("project_hash = %q, want %q", meta.ProjectHash, projHash)
	}

	got, _ := DecryptContent(blob, dek)
	if got != plaintext {
		t.Errorf("round trip lost plaintext: got %q want %q", got, plaintext)
	}
}

func TestDecryptWrongDEK(t *testing.T) {
	dekA := bytes.Repeat([]byte{0xaa}, 32)
	dekB := bytes.Repeat([]byte{0xbb}, 32)
	blob, _ := EncryptContent("secret", dekA, 1, "")
	if _, err := DecryptContent(blob, dekB); err != ErrAuthFailed {
		t.Errorf("Decrypt with wrong DEK: err = %v, want ErrAuthFailed", err)
	}
}
