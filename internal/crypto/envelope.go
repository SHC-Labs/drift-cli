package crypto

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// EnvelopePrefix is the magic string every drift-encrypted blob starts
// with. Server uses this as the cheap "is this ciphertext?" check before
// deciding whether to pass it through. Bumping the version (v2 etc) is
// a wire-format change requiring a server-side dual-version transition.
const EnvelopePrefix = "drift-e2ee-v1:"

// Envelope is the on-disk + on-wire shape of a drift-e2ee-v1 blob,
// without the prefix. ct/nonce/tag are base64 strings inside the JSON
// (matching TS); EncodeEnvelope handles the encoding.
type Envelope struct {
	V           int    // always 1 for the v1 envelope
	Ciphertext  []byte // raw ciphertext (no tag)
	Nonce       []byte // 12 bytes
	Tag         []byte // 16 bytes
	DEKVersion  int    // dek_id, optional (zero means omit)
	ProjectHash string // project_hash, optional (empty means omit)
}

// EncodeEnvelope produces the wire-format string for the given
// Envelope. Output is `drift-e2ee-v1:` + base64(JSON) where JSON field
// order is FIXED to match TS's JSON.stringify insertion order:
//
//	{v, ct, nonce, tag, dek_id?, project_hash?}
//
// Field order matters for byte-identical test vectors against the TS
// relay. We build the JSON manually because Go's encoding/json sorts
// alphabetically. The cost is ~20 lines; the win is binary-exact wire
// compat with the existing TS relay.
func EncodeEnvelope(env *Envelope) (string, error) {
	if env.V == 0 {
		env.V = 1
	}
	if env.V != 1 {
		return "", fmt.Errorf("envelope: only v1 supported in this binary, got v%d", env.V)
	}

	b64 := base64.StdEncoding
	var sb strings.Builder
	sb.WriteByte('{')
	sb.WriteString(`"v":`)
	sb.WriteString(itoa(env.V))
	sb.WriteString(`,"ct":"`)
	sb.WriteString(b64.EncodeToString(env.Ciphertext))
	sb.WriteString(`","nonce":"`)
	sb.WriteString(b64.EncodeToString(env.Nonce))
	sb.WriteString(`","tag":"`)
	sb.WriteString(b64.EncodeToString(env.Tag))
	sb.WriteString(`"`)
	if env.DEKVersion != 0 {
		sb.WriteString(`,"dek_id":`)
		sb.WriteString(itoa(env.DEKVersion))
	}
	if env.ProjectHash != "" {
		sb.WriteString(`,"project_hash":`)
		hashJSON, err := json.Marshal(env.ProjectHash)
		if err != nil {
			return "", fmt.Errorf("envelope: marshal project_hash: %w", err)
		}
		sb.Write(hashJSON)
	}
	sb.WriteByte('}')

	jsonBytes := []byte(sb.String())
	return EnvelopePrefix + b64.EncodeToString(jsonBytes), nil
}

// DecodeEnvelope reverses EncodeEnvelope. Order of fields in the inner
// JSON does not matter on decode (json.Unmarshal is order-agnostic);
// this is the path that accepts envelopes produced by either the Go
// binary or the existing TS relay.
func DecodeEnvelope(blob string) (*Envelope, error) {
	if !strings.HasPrefix(blob, EnvelopePrefix) {
		return nil, fmt.Errorf("envelope: missing %q prefix", EnvelopePrefix)
	}
	body := blob[len(EnvelopePrefix):]
	jsonBytes, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("envelope: base64 decode body: %w", err)
	}
	var raw struct {
		V           int    `json:"v"`
		CT          string `json:"ct"`
		Nonce       string `json:"nonce"`
		Tag         string `json:"tag"`
		DEKVersion  int    `json:"dek_id,omitempty"`
		ProjectHash string `json:"project_hash,omitempty"`
	}
	if err := json.Unmarshal(jsonBytes, &raw); err != nil {
		return nil, fmt.Errorf("envelope: parse JSON: %w", err)
	}
	if raw.V != 1 {
		return nil, fmt.Errorf("envelope: unsupported version %d (this binary handles v1 only)", raw.V)
	}
	ct, err := base64.StdEncoding.DecodeString(raw.CT)
	if err != nil {
		return nil, fmt.Errorf("envelope: base64 decode ct: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(raw.Nonce)
	if err != nil {
		return nil, fmt.Errorf("envelope: base64 decode nonce: %w", err)
	}
	tag, err := base64.StdEncoding.DecodeString(raw.Tag)
	if err != nil {
		return nil, fmt.Errorf("envelope: base64 decode tag: %w", err)
	}
	return &Envelope{
		V:           raw.V,
		Ciphertext:  ct,
		Nonce:       nonce,
		Tag:         tag,
		DEKVersion:  raw.DEKVersion,
		ProjectHash: raw.ProjectHash,
	}, nil
}

// IsCiphertext returns true if the value looks like a drift-e2ee blob.
// Mirrors crypto.ts isCiphertext: checks the prefix only, doesn't try
// to parse the body. Cheap fast-path for the response-substitution
// scanner.
func IsCiphertext(v string) bool {
	return strings.HasPrefix(v, EnvelopePrefix)
}

// itoa formats a small positive int for JSON without allocating via
// strconv.Itoa's fmt machinery. Sub-100 budget ints, called once per
// envelope encode.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
