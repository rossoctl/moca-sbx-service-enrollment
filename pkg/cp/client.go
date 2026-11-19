// Package cp is a minimal client for the moca control plane's members and
// sandbox routes, authenticated as the synthetic `installer` subject via the
// deployment's SH_INSTALLER_TOKEN bearer (packages/control-plane/src/server.ts).
package cp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

type Client struct {
	base      *url.URL // e.g. https://moca-sbx.apps.example.com
	token     string
	client    *http.Client
	userAgent string
}

// New builds a client for one control plane. caFile, when non-empty, is a PEM
// CA bundle for the API route's certificate — the router CA, mounted the way
// the janitor mounts it, for deployments whose certificate is not signed by a
// public CA.
func New(baseURL, token, caFile, userAgent string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("control-plane URL: %w", err)
	}
	if base.Scheme != "https" && base.Scheme != "http" {
		return nil, fmt.Errorf("control-plane URL scheme must be http or https, got %q", base.Scheme)
	}
	transport := &http.Transport{}
	if caFile != "" {
		pool, err := loadCAPool(caFile)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool} //nolint:gosec // private CA by configuration
	}
	return &Client{
		base:      base,
		token:     token,
		client:    &http.Client{Timeout: 60 * time.Second, Transport: transport},
		userAgent: userAgent,
	}, nil
}

// loadCAPool reads a PEM CA bundle from disk.
func loadCAPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in %s", caFile)
	}
	return pool, nil
}

// Subject is one entry of GET /v1/subjects' answer (member-handlers.ts listSubjects).
type Subject struct {
	Subject       string `json:"subject"`
	Enrolled      bool   `json:"enrolled"`
	Source        string `json:"source"` // "env" (settings-baked) or "api"
	StoreEnrolled bool   `json:"storeEnrolled"`
}

// Sandbox is one entry of GET /v1/sandboxes?all=1 (sandbox-handlers.ts listSandboxes).
type Sandbox struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	State string `json:"state"`
}

// Error is a control-plane refusal: an HTTP status outside 2xx with the body's
// `error` code when the control plane sent one.
type Error struct {
	Status int
	Code   string
	Body   string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("control plane answered %d %s", e.Status, e.Code)
	}
	return fmt.Sprintf("control plane answered %d: %s", e.Status, e.Body)
}

// IsRefusal distinguishes the control plane saying "no, by design" from transit
// and decode failures: a refusal is logged and the run continues, because
// working around it (editing settings, revoking roles) is not this service's job.
func IsRefusal(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}

// call does one request and returns the decoded body on 2xx, a *Error otherwise.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	status, b, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		return &Error{Status: status, Code: e.Error, Body: string(b)}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("decode %s %s answer: %w", method, path, err)
		}
	}
	return nil
}

// ListSubjects returns every enrolled subject (GET /v1/subjects, admin).
func (c *Client) ListSubjects(ctx context.Context) ([]Subject, error) {
	var resp struct {
		Subjects []Subject `json:"subjects"`
	}
	if err := c.call(ctx, http.MethodGet, "/v1/subjects", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Subjects, nil
}

// Enroll marks a subject enrolled (PUT /v1/subjects/{subject}, body
// {"enrolled": true}). The route is idempotent and answers 200 for a
// settings-baked subject too.
func (c *Client) Enroll(ctx context.Context, subject string) error {
	return c.call(ctx, http.MethodPut, "/v1/subjects/"+subject, map[string]bool{"enrolled": true}, nil)
}

// Unenroll removes a subject's enrollment (DELETE /v1/subjects/{subject}). The
// control plane refuses a settings-baked subject (409 bootstrap_subject) or one
// holding a live admin grant (409 admin_granted); both come back as *Error for
// the caller to log.
func (c *Client) Unenroll(ctx context.Context, subject string) error {
	return c.call(ctx, http.MethodDelete, "/v1/subjects/"+subject, nil, nil)
}

// ListSandboxes returns every sandbox on the deployment (GET
// /v1/sandboxes?all=1, admin-only query) — ready and terminated alike; the
// caller filters by owner.
func (c *Client) ListSandboxes(ctx context.Context) ([]Sandbox, error) {
	var resp struct {
		Sandboxes []Sandbox `json:"sandboxes"`
	}
	if err := c.call(ctx, http.MethodGet, "/v1/sandboxes?all=1", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Sandboxes, nil
}

// DeleteSandbox terminates one sandbox (DELETE /v1/sandboxes/{id}); an admin
// (the installer subject is one) may delete another user's.
func (c *Client) DeleteSandbox(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/v1/sandboxes/"+id, nil, nil)
}
