package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// apiCall sends an authenticated request to base+path and returns the status
// code and decoded JSON body. A string body is sent verbatim as YAML; any
// other non-nil body is JSON-encoded. idemKey, when non-empty, is sent as the
// Idempotency-Key header.
func apiCall(t *testing.T, base, tok, method, path string, body any, idemKey string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rdr = strings.NewReader(b)
		default:
			j, _ := json.Marshal(body)
			rdr = bytes.NewReader(j)
		}
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	if body != nil {
		ct := "application/json"
		if _, ok := body.(string); ok {
			ct = "application/yaml"
		}
		req.Header.Set("Content-Type", ct)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}
