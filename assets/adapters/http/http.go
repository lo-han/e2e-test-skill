package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTP drives the service's HTTP interface, keeping the raw answer.
//
// It deliberately does not decode into a caller's struct by default: half the
// findings are about what the service emitted — a null where a field should be
// absent, a zero where the spec says omitted — and a decode into a typed struct
// throws exactly that evidence away.
type HTTP struct {
	BaseURL string
	Client  *http.Client
	// Header is sent on every request (auth, tenant, content type).
	Header http.Header
}

// NewHTTP returns a client with a bounded timeout, never the default zero one.
func NewHTTP(baseURL string, timeout time.Duration) *HTTP {
	return &HTTP{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Client:  &http.Client{Timeout: timeout},
		Header:  http.Header{},
	}
}

// Response keeps everything a scenario might assert on.
type Response struct {
	Status  int
	Header  http.Header
	Body    []byte
	Elapsed time.Duration
}

// Text is the body as a string, for message assertions.
func (r Response) Text() string { return string(r.Body) }

// JSON decodes the body into v, reporting the raw body on failure so a
// decode error never hides what the service actually sent.
func (r Response) JSON(v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("decoding response body as JSON: %w\nbody was: %s", err, r.Body)
	}
	return nil
}

// Map decodes a JSON object, which is what most shape assertions want:
// checking a key is absent is a map lookup, and impossible on a struct.
func (r Response) Map() (map[string]any, error) {
	var m map[string]any
	return m, r.JSON(&m)
}

// List decodes a JSON array of objects.
func (r Response) List() ([]map[string]any, error) {
	var l []map[string]any
	return l, r.JSON(&l)
}

// Do sends a request. body may be nil, []byte, string, or any JSON-marshalable
// value. Passing raw bytes is how malformed-input scenarios send bytes the
// service's decoder cannot parse.
func (h *HTTP) Do(ctx context.Context, method, path string, body any) (Response, error) {
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(b)
	case string:
		reader = strings.NewReader(b)
	default:
		encoded, err := json.Marshal(b)
		if err != nil {
			return Response{}, fmt.Errorf("encoding request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, h.BaseURL+path, reader)
	if err != nil {
		return Response{}, fmt.Errorf("building %s %s: %w", method, path, err)
	}
	for k, values := range h.Header {
		for _, v := range values {
			req.Header.Add(k, v)
		}
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	started := time.Now()
	resp, err := h.Client.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, fmt.Errorf("reading %s %s response: %w", method, path, err)
	}
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: payload, Elapsed: time.Since(started)}, nil
}

// Get, Post, Put, Patch, Delete are the ordinary cases.
func (h *HTTP) Get(ctx context.Context, path string) (Response, error) {
	return h.Do(ctx, http.MethodGet, path, nil)
}
func (h *HTTP) Post(ctx context.Context, path string, body any) (Response, error) {
	return h.Do(ctx, http.MethodPost, path, body)
}
func (h *HTTP) Put(ctx context.Context, path string, body any) (Response, error) {
	return h.Do(ctx, http.MethodPut, path, body)
}
func (h *HTTP) Patch(ctx context.Context, path string, body any) (Response, error) {
	return h.Do(ctx, http.MethodPatch, path, body)
}
func (h *HTTP) Delete(ctx context.Context, path string) (Response, error) {
	return h.Do(ctx, http.MethodDelete, path, nil)
}

// Ready is the readiness probe to hand to Service.Ready: a real call that
// answers, not a bare connection.
func (h *HTTP) Ready(path string) func(context.Context) error {
	return func(ctx context.Context) error {
		resp, err := h.Get(ctx, path)
		if err != nil {
			return err
		}
		if resp.Status >= 500 {
			return fmt.Errorf("%s answered %d", path, resp.Status)
		}
		return nil
	}
}
