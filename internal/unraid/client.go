// Package unraid implements a minimal client for the Unraid GraphQL API.
package unraid

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client talks to the Unraid API GraphQL endpoint (usually https://<host>/graphql).
type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

// Options configures a Client.
type Options struct {
	// URL is the base URL of the Unraid server, e.g. "https://tower.local".
	// If it does not end in "/graphql", that path is appended.
	URL                string
	APIKey             string
	Timeout            time.Duration
	InsecureSkipVerify bool
}

// NewClient builds a Client from opts.
func NewClient(opts Options) (*Client, error) {
	if opts.URL == "" {
		return nil, errors.New("unraid URL is required")
	}
	if opts.APIKey == "" {
		return nil, errors.New("unraid API key is required")
	}
	endpoint := strings.TrimRight(opts.URL, "/")
	if !strings.HasSuffix(endpoint, "/graphql") {
		endpoint += "/graphql"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for self-signed Unraid certs
	}
	return &Client{
		endpoint: endpoint,
		apiKey:   opts.APIKey,
		http:     &http.Client{Timeout: opts.Timeout, Transport: transport},
	}, nil
}

type request struct {
	OperationName string         `json:"operationName,omitempty"`
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables,omitempty"`
}

type response struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors"`
}

// GraphQLError is a single entry of a GraphQL "errors" array.
type GraphQLError struct {
	Message string `json:"message"`
	Path    []any  `json:"path"`
}

func (e GraphQLError) Error() string {
	if len(e.Path) == 0 {
		return e.Message
	}
	parts := make([]string, len(e.Path))
	for i, p := range e.Path {
		parts[i] = fmt.Sprint(p)
	}
	return strings.Join(parts, ".") + ": " + e.Message
}

// PartialError is returned when the server responded with data and errors.
// The data has been decoded into the output value; the errors are informational
// (e.g. a nullable field that failed to resolve).
type PartialError struct {
	Errors []GraphQLError
}

func (e *PartialError) Error() string {
	msgs := make([]string, len(e.Errors))
	for i, err := range e.Errors {
		msgs[i] = err.Error()
	}
	return "graphql partial errors: " + strings.Join(msgs, "; ")
}

// Query executes a GraphQL query and decodes its "data" into out.
//
// If the response contains both data and errors, the data is decoded and a
// *PartialError is returned so callers can decide whether to keep the result.
func (c *Client) Query(ctx context.Context, operationName, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(request{OperationName: operationName, Query: query, Variables: vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}

	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unraid API returned HTTP %d: %s", resp.StatusCode, truncate(raw, 200))
		}
		return fmt.Errorf("decoding graphql response: %w", err)
	}
	hasData := len(r.Data) > 0 && string(r.Data) != "null"
	if !hasData {
		if len(r.Errors) > 0 {
			return fmt.Errorf("unraid API returned HTTP %d: %w", resp.StatusCode, errors.Join(toErrors(r.Errors)...))
		}
		return fmt.Errorf("unraid API returned HTTP %d without data", resp.StatusCode)
	}
	if err := json.Unmarshal(r.Data, out); err != nil {
		return fmt.Errorf("decoding graphql data: %w", err)
	}
	if len(r.Errors) > 0 {
		return &PartialError{Errors: r.Errors}
	}
	return nil
}

// IsPartial reports whether err only signals GraphQL errors for some fields
// while the rest of the data was decoded successfully.
func IsPartial(err error) bool {
	var pe *PartialError
	return errors.As(err, &pe)
}

func toErrors(gqlErrs []GraphQLError) []error {
	errs := make([]error, len(gqlErrs))
	for i, e := range gqlErrs {
		errs[i] = e
	}
	return errs
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// Number decodes a JSON number, a numeric string, or null.
// Unraid exposes BigInt and several capacity fields as strings.
type Number struct {
	Value float64
	Valid bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (n *Number) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		*n = Number{}
		return nil
	}
	s = strings.Trim(s, `"`)
	if s == "" || strings.EqualFold(s, "nan") {
		*n = Number{}
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// Tolerate non-numeric strings instead of failing the whole response.
		*n = Number{}
		return nil
	}
	*n = Number{Value: v, Valid: true}
	return nil
}
