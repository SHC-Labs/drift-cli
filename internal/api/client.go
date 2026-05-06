package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/SHC-Labs/drift/internal/version"
)

// Client wraps an http.Client + the upstream base URL + the Bearer
// token. Used by capabilities, install state events, OAuth login, etc.
//
// Cheap to construct; callers can build a fresh one per command. The
// HTTP client uses sane defaults (short connect timeout, default
// transport with keep-alive) and is goroutine-safe.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	Token   string
}

// NewClient builds a Client with the standard HTTP setup. baseURL
// should be scheme + host + port, no path, no trailing slash. Token
// is the raw key (without "Bearer " prefix); empty token means
// AddAuth is a no-op (used by handshake during early bootstrap when
// no token is present yet, server returns 401 if it requires auth).
func NewClient(baseURL, token string) *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
		},
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
	}
}

// URL builds an absolute URL for the given path. Path should start
// with a slash. Caller is responsible for the /v1/ prefix.
func (c *Client) URL(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return c.BaseURL + path
}

// AddAuth sets the Authorization header from c.Token. No-op when
// token is empty.
func (c *Client) AddAuth(req *http.Request) {
	if c.Token == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
}

// AddUserAgent sets the standard User-Agent header. Server middleware
// uses this to expose req.binaryVersion to handlers per the plan's
// protocol versioning section.
func (c *Client) AddUserAgent(req *http.Request) {
	req.Header.Set("User-Agent", "drift/"+version.Version+" ("+version.OSArch+")")
}
