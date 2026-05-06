package relay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SHC-Labs/drift/internal/crypto"
	"github.com/SHC-Labs/drift/internal/log"
)

// Pipeline owns the encrypt/decrypt path for MCP request/response
// bodies. Outbound: scan JSON for content fields, encrypt with the
// org DEK (or per-project DEK when a project_hash is in scope), replace
// with envelope strings. Inbound: scan response for envelope blobs,
// decrypt, substitute back.
//
// Mirrors the TS relay's encryption-pipeline.ts at the field level.
// Field paths to encrypt are determined by the JSON structure; we
// recurse and target known content-bearing keys.
type Pipeline struct {
	dek         *DEKManager
	projectDeks *ProjectDEKManager
}

// NewPipeline wires the DEK managers into a pipeline. Both must be
// non-nil; if a v1 binary doesn't have crypto wired yet, callers
// should skip pipeline construction and forward plaintext.
func NewPipeline(dek *DEKManager, projectDeks *ProjectDEKManager) *Pipeline {
	return &Pipeline{dek: dek, projectDeks: projectDeks}
}

// EncryptRequestBody walks the parsed MCP request JSON and encrypts
// every content-bearing field, returning the modified JSON bytes
// ready to forward upstream.
//
// Content fields targeted (mirrors TS encryption-pipeline.ts):
//   - tool_input.* string fields (the customer's prompts + edits)
//   - params.text, params.message, params.content
//   - any string value at a depth-2 path under "content"
//
// Project_hash, when present in tool_input, drives per-project DEK
// selection. Without it, the org DEK applies.
func (p *Pipeline) EncryptRequestBody(ctx context.Context, body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		// Not JSON, leave alone (probably a binary upload or empty).
		return body, nil
	}
	projectHash := extractProjectHash(root)
	dek, dekVersion, err := p.pickDEK(ctx, projectHash)
	if err != nil {
		return nil, err
	}
	enc := func(plain string) (string, error) {
		return crypto.EncryptContent(plain, dek, dekVersion, projectHash)
	}
	encrypted, err := walkEncrypt(root, enc)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(encrypted)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DecryptResponseBody walks the parsed MCP response JSON, finds every
// drift-e2ee-v1 envelope, decrypts with the matching DEK, and
// substitutes the plaintext back into the structure. Failures replace
// the blob with "[decrypt failed: ...]" so the agent gets a visible
// error rather than silent ciphertext.
func (p *Pipeline) DecryptResponseBody(ctx context.Context, body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	// Some responses are plain text with embedded envelopes (tool
	// output strings); others are structured JSON. Try JSON first;
	// fall back to string substitution.
	var root any
	if err := json.Unmarshal(body, &root); err == nil {
		decrypted, derr := p.walkDecrypt(ctx, root)
		if derr != nil {
			return nil, derr
		}
		out, err := json.Marshal(decrypted)
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	// Not JSON: do string-level substitution on the raw body.
	plaintext := crypto.ReplaceCiphertextInText(string(body), func(blob string) (string, error) {
		return p.decryptBlob(ctx, blob)
	})
	return []byte(plaintext), nil
}

// pickDEK chooses between the org DEK and a per-project DEK based on
// whether project_hash was extracted from the request body. Returns
// the DEK + its version for envelope tagging.
func (p *Pipeline) pickDEK(ctx context.Context, projectHash string) ([]byte, int, error) {
	if projectHash != "" && p.projectDeks != nil {
		dek, ver, err := p.projectDeks.Get(ctx, projectHash)
		if err == nil {
			return dek, ver, nil
		}
		// Fall back to org DEK on per-project fetch failure (e.g.
		// the project isn't enrolled). Log and continue.
		log.Warn("relay.pipeline", "project_dek_fallback", map[string]any{
			"project_hash": projectHash,
			"error":        err.Error(),
		})
	}
	return p.dek.Current(ctx)
}

// decryptBlob is the per-envelope decrypt path. Inspects the envelope
// to learn dek_id + project_hash, fetches the right DEK, decrypts.
func (p *Pipeline) decryptBlob(ctx context.Context, blob string) (string, error) {
	meta := crypto.InspectEnvelope(blob)
	if meta == nil {
		return blob, fmt.Errorf("inspect envelope")
	}
	var dek []byte
	var err error
	if meta.ProjectHash != "" && p.projectDeks != nil {
		dek, _, err = p.projectDeks.Get(ctx, meta.ProjectHash)
		if err != nil {
			return blob, err
		}
	} else {
		if meta.DEKVersion > 0 {
			dek, err = p.dek.ByVersion(ctx, meta.DEKVersion)
		} else {
			dek, _, err = p.dek.Current(ctx)
		}
		if err != nil {
			return blob, err
		}
	}
	return crypto.DecryptContent(blob, dek)
}

// extractProjectHash walks the root JSON looking for a "project_hash"
// string field. Returns empty string if not found. The TS relay puts
// it under tool_input.project_hash; we accept it at any depth so the
// pipeline survives schema drift.
func extractProjectHash(root any) string {
	switch v := root.(type) {
	case map[string]any:
		if ph, ok := v["project_hash"].(string); ok && ph != "" {
			return ph
		}
		for _, child := range v {
			if found := extractProjectHash(child); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range v {
			if found := extractProjectHash(child); found != "" {
				return found
			}
		}
	}
	return ""
}

// walkEncrypt recursively walks the JSON value and encrypts string
// fields at known content-bearing paths. We don't try to encrypt
// every string in the tree (would mangle field names that look like
// content); we target the specific keys the MCP protocol uses for
// user data.
//
// Encrypted keys:
//   text, message, content, prompt, instructions
//   tool_input -> all string children (per TS relay's
//   encrypt-everything-under-tool-input behavior)
func walkEncrypt(node any, enc func(string) (string, error)) (any, error) {
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, child := range v {
			if k == "tool_input" {
				encrypted, err := encryptStringsRecursive(child, enc)
				if err != nil {
					return nil, err
				}
				out[k] = encrypted
				continue
			}
			if isContentKey(k) {
				if s, ok := child.(string); ok && s != "" && !crypto.IsCiphertext(s) {
					blob, err := enc(s)
					if err != nil {
						return nil, fmt.Errorf("encrypt %s: %w", k, err)
					}
					out[k] = blob
					continue
				}
			}
			encChild, err := walkEncrypt(child, enc)
			if err != nil {
				return nil, err
			}
			out[k] = encChild
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			encChild, err := walkEncrypt(child, enc)
			if err != nil {
				return nil, err
			}
			out[i] = encChild
		}
		return out, nil
	default:
		return v, nil
	}
}

// encryptStringsRecursive encrypts every string descendant of the
// value. Used for tool_input where every string child is potentially
// content (file contents, prompts, etc).
func encryptStringsRecursive(node any, enc func(string) (string, error)) (any, error) {
	switch v := node.(type) {
	case string:
		if v == "" || crypto.IsCiphertext(v) {
			return v, nil
		}
		return enc(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, child := range v {
			// Skip metadata-shaped fields under tool_input even if
			// they're strings: project_hash, file_path, etc are not
			// content. Encrypting them would mangle the server-side
			// project routing.
			if isMetadataKey(k) {
				out[k] = child
				continue
			}
			encChild, err := encryptStringsRecursive(child, enc)
			if err != nil {
				return nil, err
			}
			out[k] = encChild
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			encChild, err := encryptStringsRecursive(child, enc)
			if err != nil {
				return nil, err
			}
			out[i] = encChild
		}
		return out, nil
	default:
		return v, nil
	}
}

// walkDecrypt recursively walks JSON and decrypts every envelope
// string it finds. Failures get replaced with "[decrypt failed: ...]"
// inline so the agent sees a clear error.
func (p *Pipeline) walkDecrypt(ctx context.Context, node any) (any, error) {
	switch v := node.(type) {
	case string:
		if !crypto.IsCiphertext(v) {
			return v, nil
		}
		plain, err := p.decryptBlob(ctx, v)
		if err != nil {
			return fmt.Sprintf("[decrypt failed: %v]", err), nil
		}
		return plain, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, child := range v {
			decChild, err := p.walkDecrypt(ctx, child)
			if err != nil {
				return nil, err
			}
			out[k] = decChild
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			decChild, err := p.walkDecrypt(ctx, child)
			if err != nil {
				return nil, err
			}
			out[i] = decChild
		}
		return out, nil
	default:
		return v, nil
	}
}

// isContentKey returns true for top-level keys we encrypt unconditionally.
func isContentKey(k string) bool {
	switch k {
	case "text", "message", "content", "prompt", "instructions":
		return true
	}
	return false
}

// isMetadataKey returns true for keys that should NOT be encrypted
// even when they appear under tool_input. These carry server-side
// routing or policy metadata; encrypting would break the server's
// dispatch logic.
func isMetadataKey(k string) bool {
	switch k {
	case "project_hash", "file_path", "tool_name", "developer_id",
		"agent_id", "intent_id", "task_id", "message_id":
		return true
	}
	return false
}
