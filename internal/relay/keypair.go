package relay

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"

	"github.com/SHC-Labs/drift/internal/api"
	"github.com/SHC-Labs/drift/internal/crypto"
	"github.com/SHC-Labs/drift/internal/keychain"
	"github.com/SHC-Labs/drift/internal/log"
)

// EnsureKeyPair returns the relay's ECDH keypair, generating + persisting
// one on first call and reusing it on subsequent calls. Privkey lives
// in the OS keychain; pubkey gets published to the server so other
// relay instances (multi-dev orgs) can wrap KEKs for this recipient.
//
// Mirrors the TS relay's KekManager.init() bootstrap path.
func EnsureKeyPair(ctx context.Context, c *api.Client) (*crypto.ECDHKeyPair, error) {
	if existingHex, err := keychain.GetPrivKey(); err == nil && existingHex != "" {
		priv, err := hex.DecodeString(existingHex)
		if err == nil && len(priv) == crypto.ECDHPrivKeyBytes {
			pub, derErr := derivePubFromPriv(priv)
			if derErr == nil {
				return &crypto.ECDHKeyPair{Priv: priv, Pub: pub}, nil
			}
		}
		// If parsing failed, fall through to regenerate.
		log.Warn("relay.keypair", "stored_priv_invalid", map[string]any{
			"reason": "regenerating",
		})
	}

	kp, err := crypto.GenerateECDHKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}
	if err := keychain.SetPrivKey(hex.EncodeToString(kp.Priv)); err != nil {
		return nil, fmt.Errorf("persist privkey: %w", err)
	}
	log.Info("relay.keypair", "generated", map[string]any{
		"fingerprint": crypto.PubKeyFingerprint(kp.Pub),
	})

	if err := publishPubkey(ctx, c, kp.Pub); err != nil {
		// Non-fatal at startup: server may not require pubkey for
		// every operation, and we'll retry on the next operation
		// that needs it.
		log.Warn("relay.keypair", "publish_failed", map[string]any{
			"error": err.Error(),
		})
	}

	return kp, nil
}

// publishPubkey POSTs the pubkey to /v1/relay/pubkey/me. Server
// stores it under the developer_id derived from the Bearer token.
func publishPubkey(ctx context.Context, c *api.Client, pubkey []byte) error {
	body := map[string]any{
		"ecdh_pubkey": hex.EncodeToString(pubkey),
		"fingerprint": crypto.PubKeyFingerprint(pubkey),
	}
	if err := c.PostJSON(ctx, "/v1/relay/pubkey/me", body); err != nil {
		return fmt.Errorf("publish pubkey: %w", err)
	}
	log.Info("relay.keypair", "published", map[string]any{
		"fingerprint": crypto.PubKeyFingerprint(pubkey),
	})
	return nil
}

// derivePubFromPriv reconstructs the pubkey for a given privkey by
// running ECDH against a known generator. This is used when we read
// a privkey from the keychain and need the matching pubkey without
// having stored it. Falls through to a fresh keypair if derivation
// fails (privkey corruption).
func derivePubFromPriv(priv []byte) ([]byte, error) {
	// Round-trip via the crypto package: GenerateECDHKeyPair gives
	// us a fresh keypair. We can't directly derive pub-from-priv
	// without the curve operation. Easiest correct way is to import
	// the privkey via crypto/ecdh.P256().NewPrivateKey() and read
	// PublicKey().Bytes(). That's what crypto/ecdh.go does internally
	// but we don't expose it; instead, use the path that already
	// works: load the priv into ECDH, derive against the generator
	// via DeriveSharedSecret with a known peer pubkey -- no, that
	// gives a shared secret, not the matching pubkey.
	//
	// Real path: the stdlib crypto/ecdh exposes PublicKey() on a
	// PrivateKey. We need to import it directly here.
	return cryptoEcdhPubFromPriv(priv)
}

// cryptoEcdhPubFromPriv calls into stdlib crypto/ecdh to derive the
// pubkey bytes from privkey bytes. Lifted into its own helper so the
// dependency on crypto/ecdh stays explicit and we don't import it
// from random places in the relay package.
func cryptoEcdhPubFromPriv(priv []byte) ([]byte, error) {
	if len(priv) != crypto.ECDHPrivKeyBytes {
		return nil, errors.New("priv length mismatch")
	}
	return crypto.PubFromPriv(priv)
}

// httpStatusOK is an inline helper since we don't import net/http
// elsewhere in this file. Kept for potential future status-aware
// logging in publishPubkey.
var _ = http.StatusOK
