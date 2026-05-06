package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// LoginResult is what Login returns on success: the API token plus
// some metadata about the user/org for the welcome message.
type LoginResult struct {
	Token        string `json:"token"`
	UserEmail    string `json:"user_email,omitempty"`
	OrgName      string `json:"org_name,omitempty"`
	DeveloperID  string `json:"developer_id,omitempty"`
	OrgID        string `json:"org_id,omitempty"`
}

// LoginCallbackPort is the port range we try for the localhost
// callback listener. Browsers redirect to http://127.0.0.1:<port>/cb
// during the OAuth dance.
const (
	LoginCallbackPortLow  = 53000
	LoginCallbackPortHigh = 53050
)

// LoginTimeout caps the whole login flow. Browser must complete the
// auth exchange within this; otherwise we give up so a stalled
// browser doesn't hold the binary forever.
const LoginTimeout = 5 * time.Minute

// Login runs the OAuth PKCE login flow:
//
//  1. Pick a free localhost port for the callback.
//  2. Generate a code_verifier (random 96 bytes) and code_challenge
//     (SHA-256 of verifier, URL-base64 encoded).
//  3. Open the user's browser to the dashboard's /cli/login URL with
//     state, code_challenge, and the localhost callback URL.
//  4. Wait for the browser to POST back the code + state.
//  5. Exchange the code + verifier at /v1/cli/token to get the API
//     token.
//  6. Return the token (caller stores in keychain).
//
// browserOpener defaults to opening the OS default browser; tests
// inject a fake to skip the browser step.
func Login(ctx context.Context, baseURL string, browserOpener func(string) error) (*LoginResult, error) {
	verifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("pkce: generate verifier: %w", err)
	}
	challenge := codeChallengeS256(verifier)
	state, err := randomURLSafe(32)
	if err != nil {
		return nil, fmt.Errorf("pkce: state: %w", err)
	}

	listener, callbackURL, err := openCallbackListener()
	if err != nil {
		return nil, fmt.Errorf("login: callback listener: %w", err)
	}
	defer listener.Close()

	// Build the browser URL the user gets sent to.
	loginURL, err := buildLoginURL(baseURL, callbackURL, state, challenge)
	if err != nil {
		return nil, err
	}

	// Open the browser. If browserOpener fails, we still print the
	// URL so the customer can copy it manually (headless servers).
	if browserOpener != nil {
		_ = browserOpener(loginURL)
	}

	code, err := waitForCallback(ctx, listener, state)
	if err != nil {
		return nil, err
	}

	return exchangeCode(ctx, baseURL, code, verifier)
}

// generateCodeVerifier returns 96 random bytes URL-base64-encoded.
// 96 bytes = 128 base64 chars, comfortably above the OAuth spec's
// 43-128 char range. crypto/rand source.
func generateCodeVerifier() (string, error) {
	return randomURLSafe(96)
}

// randomURLSafe reads n bytes from crypto/rand and base64url-encodes
// them. URL-safe alphabet (no =, no +, no /).
func randomURLSafe(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// codeChallengeS256 returns base64url(SHA-256(verifier)). The standard
// PKCE S256 transform.
func codeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// openCallbackListener picks the first free port in the LoginCallback
// range and returns a TCP listener on it plus the http://... URL the
// dashboard should redirect to.
func openCallbackListener() (net.Listener, string, error) {
	for port := LoginCallbackPortLow; port <= LoginCallbackPortHigh; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		return l, "http://" + addr + "/cb", nil
	}
	return nil, "", fmt.Errorf("no free port in [%d, %d]", LoginCallbackPortLow, LoginCallbackPortHigh)
}

// buildLoginURL constructs the dashboard URL the user opens in their
// browser. Includes state + code_challenge + callback URL as query
// params.
func buildLoginURL(baseURL, callback, state, challenge string) (string, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/cli/login")
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("redirect_uri", callback)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// waitForCallback runs a tiny HTTP server on listener until it
// receives a GET /cb request with the matching state. Returns the
// code from the query string.
func waitForCallback(ctx context.Context, listener net.Listener, expectedState string) (string, error) {
	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)
	once := sync.Once{}

	mux := http.NewServeMux()
	mux.HandleFunc("/cb", func(w http.ResponseWriter, r *http.Request) {
		gotState := r.URL.Query().Get("state")
		gotCode := r.URL.Query().Get("code")
		gotErr := r.URL.Query().Get("error")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if gotErr != "" {
			_, _ = io.WriteString(w, callbackPage("Login failed: "+gotErr))
			once.Do(func() { ch <- result{err: fmt.Errorf("login error from server: %s", gotErr)} })
			return
		}
		if gotState != expectedState {
			_, _ = io.WriteString(w, callbackPage("Login failed: state mismatch"))
			once.Do(func() { ch <- result{err: errors.New("state mismatch (CSRF check failed)")} })
			return
		}
		if gotCode == "" {
			_, _ = io.WriteString(w, callbackPage("Login failed: no code returned"))
			once.Do(func() { ch <- result{err: errors.New("no code in callback")} })
			return
		}
		_, _ = io.WriteString(w, callbackPage("Login successful. You can close this tab."))
		once.Do(func() { ch <- result{code: gotCode} })
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)

	select {
	case <-ctx.Done():
		_ = srv.Close()
		return "", ctx.Err()
	case <-time.After(LoginTimeout):
		_ = srv.Close()
		return "", fmt.Errorf("login timed out after %v", LoginTimeout)
	case r := <-ch:
		_ = srv.Close()
		return r.code, r.err
	}
}

// callbackPage returns a tiny HTML response the browser sees after
// the OAuth dance completes. No CSS, no images; the user closes the
// tab manually.
func callbackPage(msg string) string {
	return `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Drift CLI</title></head><body style="font-family:system-ui,sans-serif;padding:2rem;max-width:32rem;margin:0 auto"><h1>Drift CLI</h1><p>` + msg + `</p></body></html>`
}

// exchangeCode swaps the auth code + verifier for the API token at
// /v1/cli/token. Returns the LoginResult on success.
func exchangeCode(ctx context.Context, baseURL, code, verifier string) (*LoginResult, error) {
	body := map[string]string{
		"code":          code,
		"code_verifier": verifier,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/v1/cli/token",
		strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("token exchange HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var out LoginResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if out.Token == "" {
		return nil, errors.New("token exchange succeeded but no token in response")
	}
	return &out, nil
}
