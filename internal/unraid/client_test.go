package unraid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNumberUnmarshal(t *testing.T) {
	cases := []struct {
		in    string
		want  float64
		valid bool
	}{
		{`123`, 123, true},
		{`"456"`, 456, true},
		{`"12.5"`, 12.5, true},
		{`null`, 0, false},
		{`""`, 0, false},
		{`"NaN"`, 0, false},
		{`"abc"`, 0, false},
		{`"18446744073709551615"`, 18446744073709551615, true},
	}
	for _, c := range cases {
		var n Number
		if err := json.Unmarshal([]byte(c.in), &n); err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if n.Valid != c.valid || n.Value != c.want {
			t.Errorf("%s: got %+v, want %v valid=%v", c.in, n, c.want, c.valid)
		}
	}
}

func TestQuery(t *testing.T) {
	var gotKey, gotOp string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		var req request
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotOp = req.OperationName
		switch req.OperationName {
		case "Ok":
			_, _ = w.Write([]byte(`{"data":{"online":true}}`))
		case "Partial":
			_, _ = w.Write([]byte(`{"data":{"online":true},"errors":[{"message":"boom","path":["x",0]}]}`))
		case "NoData":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"Cannot query field"}]}`))
		default:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html>bad gateway</html>`))
		}
	}))
	defer srv.Close()

	c, err := NewClient(Options{URL: srv.URL + "/", APIKey: "k", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if c.endpoint != srv.URL+"/graphql" {
		t.Errorf("endpoint = %q", c.endpoint)
	}
	var out struct{ Online bool }
	ctx := context.Background()

	if err := c.Query(ctx, "Ok", "query Ok { online }", nil, &out); err != nil || !out.Online {
		t.Errorf("Ok: err=%v out=%+v", err, out)
	}
	if gotKey != "k" || gotOp != "Ok" {
		t.Errorf("headers/op not sent: key=%q op=%q", gotKey, gotOp)
	}

	out.Online = false
	err = c.Query(ctx, "Partial", "query Partial { online }", nil, &out)
	if !IsPartial(err) || !out.Online || !strings.Contains(err.Error(), "x.0: boom") {
		t.Errorf("Partial: err=%v out=%+v", err, out)
	}

	err = c.Query(ctx, "NoData", "query NoData { online }", nil, &out)
	if err == nil || IsPartial(err) || !strings.Contains(err.Error(), "Cannot query field") {
		t.Errorf("NoData: err=%v", err)
	}

	err = c.Query(ctx, "Html", "query Html { online }", nil, &out)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("Html: err=%v", err)
	}
}

func TestNewClientValidation(t *testing.T) {
	if _, err := NewClient(Options{APIKey: "k"}); err == nil {
		t.Error("expected error without URL")
	}
	if _, err := NewClient(Options{URL: "http://x"}); err == nil {
		t.Error("expected error without API key")
	}
}
