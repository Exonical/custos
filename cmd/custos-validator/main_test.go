package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// TestHandler exercises the HTTP surface without requiring the
// shellcheck binary; the full path is covered when shellcheck is on
// PATH (see TestShellcheckEndToEnd).
func TestHandler(t *testing.T) {
	h := handler("test", make(chan struct{}, 4))

	// healthz
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if r.Code != http.StatusOK {
		t.Fatalf("healthz: %d", r.Code)
	}

	// wrong method / unknown path
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/v1/shellcheck", nil))
	if r.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method: %d", r.Code)
	}

	// oversized body
	r = httptest.NewRecorder()
	big := bytes.Repeat([]byte("x"), maxBody+1)
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/shellcheck",
		bytes.NewReader(big)))
	if r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("limit: %d", r.Code)
	}

	// unsupported shell is rejected before exec
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/shellcheck",
		strings.NewReader(`{"shell":"zsh","script":"echo hi"}`)))
	if r.Code != http.StatusBadRequest {
		t.Fatalf("shell: %d", r.Code)
	}
}

func TestShellcheckEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("shellcheck"); err != nil {
		t.Skip("shellcheck not on PATH")
	}
	h := handler(detectVersion(), make(chan struct{}, 4))
	r := httptest.NewRecorder()
	body := `{"shell":"bash","script":"#!/bin/bash\necho $x\n"}`
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/shellcheck",
		strings.NewReader(body)))
	if r.Code != http.StatusOK {
		t.Fatalf("%d: %s", r.Code, r.Body.String())
	}
	var out struct {
		Version string `json:"version"`
		Result  struct {
			Comments []struct {
				Code int `json:"code"`
			} `json:"comments"`
		} `json:"result"`
	}
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Version == "" {
		t.Error("version missing")
	}
}

func TestSaturated(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // occupied
	h := handler("test", sem)
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/v1/shellcheck",
		strings.NewReader(`{"shell":"bash","script":"x"}`)))
	if r.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when saturated, got %d", r.Code)
	}
	// healthz stays responsive under saturation.
	r = httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if r.Code != http.StatusOK {
		t.Fatalf("healthz under saturation: %d", r.Code)
	}
}
