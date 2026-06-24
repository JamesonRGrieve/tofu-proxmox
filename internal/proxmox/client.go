// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Package proxmox is a minimal client for the Proxmox product family REST API
// (/api2/json over HTTPS) — PVE, PBS, PMG, and PDM. It is generic over that API
// surface: any path can be GET/POST/PUT/DELETE'd, and the provider builds its
// two resources (proxmox_object, proxmox_task) on top. The package has zero
// terraform dependencies.
//
// Auth is either an API token (preferred; stateless, no CSRF) or a login ticket
// (cookie + CSRFPreventionToken on writes). PMG supports tickets only.
package proxmox

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client is an authenticated Proxmox REST client. It logs in lazily on the first
// call in ticket mode (token mode is stateless) and is safe for concurrent use;
// the provider shares one Client across all resources bound to a given endpoint.
type Client struct {
	base string // e.g. https://203.0.113.2:8006/api2/json
	spec productSpec
	cfg  Config
	http *http.Client

	mu     sync.Mutex // guards ticket/csrf (ticket mode lazy login + re-auth)
	ticket string     // "<cookieName>=<ticket>"
	csrf   string     // CSRFPreventionToken, sent on POST/PUT/DELETE

	// writeMu serializes mutation sequences. PVE locks guest config server-side
	// and rejects concurrent same-guest ops; serializing all writes from one
	// client (one host) is cheap insurance under parallel applies.
	writeMu sync.Mutex

	// SSH is the node-OS transport for proxmox_host_config (the Debian settings
	// with no /api2/json endpoint). nil when the provider was configured without
	// an ssh_host — the API resources (proxmox_object/proxmox_task) never use it.
	SSH *SSHClient
}

// Config configures a Client. Exactly one auth mode must be supplied: API token
// (TokenID + TokenSecret) or ticket (Username + Password).
type Config struct {
	Product     Product
	Host        string // host or host:port, no scheme
	Port        int    // 0 → product default (PVE/PMG/PDM 8006, PBS 8007)
	Username    string // user@realm, for ticket auth
	Password    string
	TokenID     string // user@realm!tokenid, for API-token auth
	TokenSecret string
	Insecure    bool          // skip TLS verification (Proxmox ships a self-signed cert)
	Timeout     time.Duration // per request (default 30s)

	// SSH transport for proxmox_host_config (key/cert auth only, never sshpass).
	// When SSHHost is empty no SSHClient is built and the host-config resource
	// errors if used. SSHHost defaults to the API Host (stripped of any API port)
	// when left unset by the caller but provided here as a distinct address so a
	// relay/jump endpoint can differ from the API endpoint.
	SSHHost    string
	SSHPort    int
	SSHUser    string
	SSHKeyFile string
	SSHKeyPEM  string
}

// NewClient builds a Client. It validates the product and auth mode but does not
// contact the server until the first API call.
func NewClient(cfg Config) (*Client, error) {
	spec, ok := specFor(cfg.Product)
	if !ok {
		return nil, fmt.Errorf("proxmox: unknown product %q (want pve|pbs|pmg|pdm)", cfg.Product)
	}
	if cfg.TokenID != "" && !spec.supportsToken() {
		return nil, fmt.Errorf("proxmox: product %q does not support API tokens — use username/password (ticket auth)", cfg.Product)
	}
	if cfg.TokenID == "" && cfg.Username == "" {
		return nil, fmt.Errorf("proxmox: no credentials — set api_token_id+api_token_secret or username+password")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	host := strings.TrimSuffix(cfg.Host, "/")
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if !strings.Contains(host, ":") {
		port := cfg.Port
		if port == 0 {
			port = spec.defaultPort
		}
		host = fmt.Sprintf("%s:%d", host, port)
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}, //nolint:gosec // self-signed mgmt cert
	}
	c := &Client{
		base: fmt.Sprintf("https://%s/api2/json", host),
		spec: spec,
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout, Transport: tr},
	}
	if cfg.SSHHost != "" {
		c.SSH = NewSSHClient(SSHConfig{
			Host:    cfg.SSHHost,
			Port:    cfg.SSHPort,
			User:    cfg.SSHUser,
			KeyFile: cfg.SSHKeyFile,
			KeyPEM:  cfg.SSHKeyPEM,
		})
	}
	return c, nil
}

// APIError is returned when Proxmox responds with a non-2xx status.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("proxmox %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// NotFound reports whether err is an APIError with a 404 status.
func NotFound(err error) bool {
	if e, ok := err.(*APIError); ok {
		return e.Status == http.StatusNotFound
	}
	return false
}

// LockWrites/UnlockWrites bracket a mutation sequence so concurrent applies do
// not race on the same server (PVE rejects concurrent locks on a guest).
func (c *Client) LockWrites()   { c.writeMu.Lock() }
func (c *Client) UnlockWrites() { c.writeMu.Unlock() }

func (c *Client) usingToken() bool { return c.cfg.TokenID != "" }

// login obtains a ticket + CSRF token. Caller must hold c.mu.
func (c *Client) login() error {
	form := url.Values{"username": {c.cfg.Username}, "password": {c.cfg.Password}}
	req, err := http.NewRequest(http.MethodPost, c.base+"/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox login: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return &APIError{Method: "POST", Path: "/access/ticket", Status: resp.StatusCode, Body: string(raw)}
	}
	var out struct {
		Data struct {
			Ticket string `json:"ticket"`
			CSRF   string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Data.Ticket == "" {
		return fmt.Errorf("proxmox login: no ticket in response: %s", string(raw))
	}
	c.ticket = c.spec.cookieName + "=" + out.Data.Ticket
	c.csrf = out.Data.CSRF
	return nil
}

// do performs one authenticated request, returning the unwrapped `data` field of
// the Proxmox response envelope (Proxmox wraps every result as {"data": ...}).
// path is relative to /api2/json and must start with "/". body may be nil.
func (c *Client) do(method, path string, body []byte) ([]byte, error) {
	if c.usingToken() {
		raw, status, err := c.attempt(method, path, body)
		if err != nil {
			return nil, err
		}
		if status/100 != 2 {
			return nil, &APIError{Method: method, Path: path, Status: status, Body: string(raw)}
		}
		return unwrapData(raw), nil
	}

	// Ticket mode: lazy login, re-login once on 401 (ticket TTL ~2h).
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ticket == "" {
		if err := c.login(); err != nil {
			return nil, err
		}
	}
	raw, status, err := c.attempt(method, path, body)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		c.ticket, c.csrf = "", ""
		if err := c.login(); err != nil {
			return nil, err
		}
		raw, status, err = c.attempt(method, path, body)
		if err != nil {
			return nil, err
		}
	}
	if status/100 != 2 {
		return nil, &APIError{Method: method, Path: path, Status: status, Body: string(raw)}
	}
	return unwrapData(raw), nil
}

// attempt issues a single HTTP request with the active auth headers. In ticket
// mode the caller holds c.mu (so reading c.ticket/c.csrf is safe).
func (c *Client) attempt(method, path string, body []byte) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.usingToken() {
		req.Header.Set("Authorization", c.spec.authorization(c.cfg.TokenID, c.cfg.TokenSecret))
	} else {
		req.Header.Set("Cookie", c.ticket)
		if method != http.MethodGet {
			req.Header.Set("CSRFPreventionToken", c.csrf)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("proxmox %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode, nil
}

// unwrapData returns the inner `data` value of a Proxmox response envelope, or
// the raw body if there is no `data` key.
func unwrapData(raw []byte) []byte {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Data != nil {
		return env.Data
	}
	return raw
}

// Get fetches a resource. path is relative to /api2/json (must start with "/").
func (c *Client) Get(path string) ([]byte, error) { return c.do(http.MethodGet, path, nil) }

// Post creates/invokes against a collection or action path with a JSON body.
func (c *Client) Post(path string, body []byte) ([]byte, error) {
	return c.do(http.MethodPost, path, body)
}

// Put updates/sets config at the given path with a JSON body (PVE merges keys).
func (c *Client) Put(path string, body []byte) ([]byte, error) {
	return c.do(http.MethodPut, path, body)
}

// Delete removes a resource.
func (c *Client) Delete(path string) ([]byte, error) { return c.do(http.MethodDelete, path, nil) }
