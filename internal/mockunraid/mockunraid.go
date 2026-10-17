// Package mockunraid serves canned Unraid GraphQL responses for tests.
package mockunraid

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

var opRe = regexp.MustCompile(`^\s*query\s+(\w+)`)

// Handler returns an http.Handler that answers POST /graphql with the file
// <dir>/<operationName>.json. Requests must carry the given x-api-key.
func Handler(dir, apiKey string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("x-api-key") != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"message":"API key validation failed"}],"data":null}`))
			return
		}
		var req struct {
			OperationName string `json:"operationName"`
			Query         string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		op := req.OperationName
		if op == "" {
			if m := opRe.FindStringSubmatch(req.Query); m != nil {
				op = m[1]
			}
		}
		b, err := os.ReadFile(filepath.Join(dir, filepath.Base(op)+".json")) //nolint:gosec // test fixture lookup, name sanitized by Base
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"message":"Cannot query field for operation ` + op + `"}],"data":null}`))
			return
		}
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}
