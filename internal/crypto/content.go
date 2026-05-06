package crypto

import (
	"fmt"
	"regexp"
)

// EncryptContent encrypts plaintext under the 32-byte DEK and returns
// the wire-format string (drift-e2ee-v1: prefix + base64 envelope).
// Mirrors crypto.ts encryptContent byte-for-byte:
//
//	encryptContent(plaintext, dek, dekVersion=1, projectHash?) -> envelope
//
// dekVersion zero is treated as 1 to match the TS default.
func EncryptContent(plaintext string, dek []byte, dekVersion int, projectHash string) (string, error) {
	if err := EnsureKeyLen(dek, 32, "dek"); err != nil {
		return "", err
	}
	if dekVersion == 0 {
		dekVersion = 1
	}
	aead, err := NewAESGCM256(dek)
	if err != nil {
		return "", err
	}
	nonce, err := RandomNonce()
	if err != nil {
		return "", err
	}
	sealed, err := aead.Seal([]byte(plaintext), nonce, nil)
	if err != nil {
		return "", err
	}
	if len(sealed) < 16 {
		return "", fmt.Errorf("encrypt: sealed output too short (%d bytes)", len(sealed))
	}
	cut := len(sealed) - 16
	env := &Envelope{
		V:           1,
		Ciphertext:  sealed[:cut],
		Nonce:       nonce,
		Tag:         sealed[cut:],
		DEKVersion:  dekVersion,
		ProjectHash: projectHash,
	}
	return EncodeEnvelope(env)
}

// DecryptContent reverses EncryptContent. Takes the wire-format blob
// (drift-e2ee-v1: prefix + base64 envelope) and the 32-byte DEK,
// returns the plaintext or an error on tag mismatch / wrong DEK / bad
// envelope shape.
//
// Mirrors crypto.ts decryptContent.
func DecryptContent(blob string, dek []byte) (string, error) {
	if err := EnsureKeyLen(dek, 32, "dek"); err != nil {
		return "", err
	}
	env, err := DecodeEnvelope(blob)
	if err != nil {
		return "", err
	}
	if len(env.Nonce) != 12 {
		return "", fmt.Errorf("decrypt: invalid nonce length %d", len(env.Nonce))
	}
	if len(env.Tag) != 16 {
		return "", fmt.Errorf("decrypt: invalid tag length %d", len(env.Tag))
	}
	aead, err := NewAESGCM256(dek)
	if err != nil {
		return "", err
	}
	cipherWithTag := append(append([]byte{}, env.Ciphertext...), env.Tag...)
	out, err := aead.Open(cipherWithTag, env.Nonce, nil)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// InspectEnvelope peeks at envelope metadata without unwrapping the
// ciphertext. Returns the dek_id and project_hash fields, or nil on a
// malformed envelope. Mirrors crypto.ts inspectEnvelope. Used by the
// relay's response-decrypt path to figure out which DEK to fetch (org
// vs per-project) before doing the AES work.
type EnvelopeMeta struct {
	DEKVersion  int
	ProjectHash string
}

func InspectEnvelope(blob string) *EnvelopeMeta {
	env, err := DecodeEnvelope(blob)
	if err != nil {
		return nil
	}
	return &EnvelopeMeta{
		DEKVersion:  env.DEKVersion,
		ProjectHash: env.ProjectHash,
	}
}

// blobRegex is the regex the response-decrypt scanner uses to find every
// drift-e2ee-v1 blob inside a larger string. Mirrors crypto.ts PREFIX_RE,
// including the base64 character class.
var blobRegex = regexp.MustCompile(`drift-e2ee-v1:[A-Za-z0-9+/=]+`)

// FindCiphertextBlobs returns every drift-e2ee-v1 blob occurring in
// text. Mirrors crypto.ts findCiphertextBlobs. The relay uses this on
// inbound responses to find ciphertext markers, decrypt each, and
// substitute the plaintext back via ReplaceCiphertextInText.
func FindCiphertextBlobs(text string) []string {
	return blobRegex.FindAllString(text, -1)
}

// ReplaceCiphertextInText scans text for every drift-e2ee-v1 blob and
// replaces it with the result of decrypt(blob). On decrypt failure the
// blob is replaced with "[decrypt failed: <reason>]" so the agent gets
// a visible signal instead of silently-passing-through ciphertext.
//
// Mirrors crypto.ts replaceCiphertextInText.
func ReplaceCiphertextInText(text string, decrypt func(blob string) (string, error)) string {
	return blobRegex.ReplaceAllStringFunc(text, func(blob string) string {
		out, err := decrypt(blob)
		if err != nil {
			return fmt.Sprintf("[decrypt failed: %v]", err)
		}
		return out
	})
}
