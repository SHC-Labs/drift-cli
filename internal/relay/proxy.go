// Package relay is the local HTTP reverse proxy with end-to-end
// encryption on MCP content fields. Listens on 127.0.0.1:<port>,
// encrypts request bodies with the org DEK before forwarding upstream,
// decrypts response bodies before returning to the MCP client. Bearer
// auth lives in the keychain; the inbound request from the MCP client
// is unauthenticated (localhost trust).
//
// Run via Run(ctx, listener), called from internal/cli's _service
// subcommand which kardianos/service invokes when the OS service
// manager starts the binary.
package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/SHC-Labs/drift/internal/api"
	"github.com/SHC-Labs/drift/internal/keychain"
	"github.com/SHC-Labs/drift/internal/log"
	"github.com/SHC-Labs/drift/internal/version"
)

// DefaultUpstream is the canonical Drift MCP endpoint. The relay forwards
// every request here. Override with Options.Upstream for testing.
const DefaultUpstream = "https://mcp.driftlabs.io"

// Options configures a relay run. Defaults work for production; tests
// override Upstream and TokenLoader.
type Options struct {
	// Upstream is the URL prefix to forward all requests to. Should be
	// scheme + host + port, no path, no trailing slash.
	Upstream string

	// TokenLoader returns the Bearer token to inject into outbound
	// requests. Defaults to keychain.GetToken if nil. Tests inject a
	// fixed token via this hook.
	TokenLoader func() (string, error)

	// IdleTimeout caps how long an idle keep-alive connection stays
	// open. The relay is local-only so we keep this short to free fds
	// quickly when the MCP client disconnects.
	IdleTimeout time.Duration

	// DisableE2EE skips encrypt/decrypt on the request/response bodies.
	// Set when the upstream server lacks /v1/relay/* endpoints (e.g.
	// staging without crypto wired up) so the binary still works as a
	// plain auth proxy. Default false: E2EE is on.
	DisableE2EE bool
}

// Run owns the listener for the lifetime of ctx. Returns when ctx is
// done or the server hits an unrecoverable error. Caller is responsible
// for closing the listener if Run returns early; the function does not
// own listener lifecycle.
//
// Service entrypoint: drift _service calls this with the listener
// returned by ipc.BindHardened on the persisted port.
func Run(ctx context.Context, listener net.Listener, opts Options) error {
	if opts.Upstream == "" {
		opts.Upstream = DefaultUpstream
	}
	if opts.TokenLoader == nil {
		opts.TokenLoader = keychain.GetToken
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 30 * time.Second
	}

	upstreamURL, err := url.Parse(opts.Upstream)
	if err != nil {
		return fmt.Errorf("parse upstream %q: %w", opts.Upstream, err)
	}
	if upstreamURL.Scheme == "" || upstreamURL.Host == "" {
		return fmt.Errorf("upstream %q missing scheme or host", opts.Upstream)
	}

	log.Info("relay", "starting", map[string]any{
		"upstream":     opts.Upstream,
		"local_addr":   listener.Addr().String(),
		"idle_timeout": opts.IdleTimeout.String(),
		"e2ee":         !opts.DisableE2EE,
	})

	// E2EE pipeline setup. Skips silently if DisableE2EE or if the
	// keychain is missing the token/keypair (relay can run as plain
	// auth proxy until login completes).
	var pipeline *Pipeline
	if !opts.DisableE2EE {
		if p := setupPipeline(ctx, opts); p != nil {
			pipeline = p
		}
	}

	handler := buildHandler(upstreamURL, opts, pipeline)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       opts.IdleTimeout,
	}

	// Shutdown on ctx cancel. Graceful: ongoing requests get 5s to
	// drain, then the listener closes hard.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("relay serve: %w", err)
	}
	return nil
}

// buildHandler wires the reverse proxy with E2EE on the request +
// response bodies. Director rewrites the inbound request to point at
// upstream and injects the Bearer token; ModifyResponse decrypts
// envelopes in the response.
//
// Pre-director step (in the wrapping handler): read the request body,
// encrypt content fields, replace the body buffer. ModifyResponse
// reads the response body, decrypts envelopes, replaces.
func buildHandler(upstream *url.URL, opts Options, pipeline *Pipeline) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(upstream)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Strip any inbound Authorization header from the MCP client
		// (some clients send a bogus "Bearer YOUR_DRIFT_TOKEN" they
		// read out of ~/.mcp.json before drift install rewrote it).
		req.Header.Del("Authorization")

		token, err := opts.TokenLoader()
		if err == nil && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		// User-Agent identifies this binary to the server. Server
		// middleware exposes req.binaryVersion based on this header
		// per the plan's protocol versioning section.
		req.Header.Set("User-Agent", userAgent())
		// Strip Host header so upstream sees the upstream host, not
		// 127.0.0.1.
		req.Host = upstream.Host
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Error("relay", "upstream_error", map[string]any{
			"path":  r.URL.Path,
			"error": err.Error(),
		})
		// Bad-gateway when upstream is unreachable. Body is short and
		// machine-readable so MCP clients can surface a useful error.
		http.Error(w, fmt.Sprintf("drift relay: upstream unreachable: %v", err), http.StatusBadGateway)
	}

	if pipeline != nil {
		proxy.ModifyResponse = func(resp *http.Response) error {
			if resp.Body == nil {
				return nil
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				return err
			}
			decrypted, err := pipeline.DecryptResponseBody(resp.Request.Context(), body)
			if err != nil {
				log.Warn("relay", "decrypt_response_failed", map[string]any{
					"error": err.Error(),
				})
				decrypted = body
			}
			resp.Body = io.NopCloser(bytes.NewReader(decrypted))
			resp.ContentLength = int64(len(decrypted))
			resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(decrypted)))
			return nil
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	if pipeline != nil {
		mux.HandleFunc("/", encryptingHandler(pipeline, proxy))
	} else {
		mux.Handle("/", proxy)
	}
	return mux
}

// encryptingHandler wraps the reverse proxy with request-side
// encryption: read the body, encrypt content fields, replace.
func encryptingHandler(pipeline *Pipeline, proxy *httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Method != http.MethodGet {
			body, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				http.Error(w, "read body", http.StatusBadRequest)
				return
			}
			encrypted, err := pipeline.EncryptRequestBody(r.Context(), body)
			if err != nil {
				log.Warn("relay", "encrypt_request_failed", map[string]any{
					"path":  r.URL.Path,
					"error": err.Error(),
				})
				encrypted = body
			}
			r.Body = io.NopCloser(bytes.NewReader(encrypted))
			r.ContentLength = int64(len(encrypted))
			r.Header.Set("Content-Length", fmt.Sprintf("%d", len(encrypted)))
		}
		proxy.ServeHTTP(w, r)
	}
}

// setupPipeline tries to build the encryption pipeline. Returns nil
// if any prerequisite is missing (no token, no install_id, server
// rejects pubkey publish). The relay then runs as a plain auth proxy
// without E2EE; the next service restart re-tries setup.
func setupPipeline(ctx context.Context, opts Options) *Pipeline {
	token, err := opts.TokenLoader()
	if err != nil || token == "" {
		log.Info("relay.pipeline", "skip_no_token", nil)
		return nil
	}
	client := api.NewClient(opts.Upstream, token)
	keys, err := EnsureKeyPair(ctx, client)
	if err != nil {
		log.Warn("relay.pipeline", "keypair_failed", map[string]any{
			"error": err.Error(),
		})
		return nil
	}
	kek := NewKEKManager(client, keys)
	dek := NewDEKManager(client, kek)
	projectDeks := NewProjectDEKManager(client, kek, dek)
	log.Info("relay.pipeline", "ready", nil)
	return NewPipeline(dek, projectDeks)
}

// healthHandler responds with a tiny JSON ack. drift relay status uses
// this; the cmd_relay_start probe in legacy CLI checks for it (so we
// have to ship the same path for back-compat with anyone still on the
// bash CLI during the cutover window).
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, `{"status":"ok","binary":"drift","version":"`+version.Version+`"}`+"\n")
}

// userAgent returns the User-Agent header value the relay sends to
// upstream. Format: "drift/<version> (<os/arch>)".
func userAgent() string {
	return strings.TrimSpace(
		"drift/" + version.Version + " (" + version.OSArch + ")",
	)
}
