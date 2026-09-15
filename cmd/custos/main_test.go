package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func noEnv(string) (string, bool) { return "", false }

func TestVersion(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run(context.Background(), []string{"version"}, &out, &errBuf, noEnv)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "custos dev") {
		t.Fatalf("version output = %q", out.String())
	}
}

func TestServeConfigFailure(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run(context.Background(), []string{"serve"}, &out, &errBuf,
		func(k string) (string, bool) {
			if k == "CUSTOS_SERVER__LISTEN" {
				return "not-an-addr", true
			}
			return "", false
		})
	if code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(errBuf.String(), "server.listen") {
		t.Fatalf("field name missing from stderr: %q", errBuf.String())
	}
}

func TestUnknownSubcommand(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run(context.Background(), []string{"bogus"}, &out, &errBuf, noEnv); code != 2 {
		t.Fatalf("exit = %d", code)
	}
}
