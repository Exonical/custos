package main

import (
	"fmt"
	"io"
	"runtime"
)

// Set via -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func cmdVersion(w io.Writer) int {
	_, _ = fmt.Fprintf(w, "custos %s (commit %s, built %s, %s)\n",
		version, commit, date, runtime.Version())
	return 0
}
