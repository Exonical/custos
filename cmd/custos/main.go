// custos is the single Custos binary. Subcommands: serve, worker,
// migrate, version.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Exonical/custos/internal/platform/apperr"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv))
}

// run is the testable entry point; it returns the process exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	fs := flag.NewFlagSet("custos", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to config file (env CUSTOS_CONFIG)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" {
		*configPath, _ = lookupEnv("CUSTOS_CONFIG")
	}

	switch fs.Arg(0) {
	case "version":
		return cmdVersion(stdout)
	case "serve":
		return cmdServe(ctx, *configPath, lookupEnv, stderr)
	case "worker":
		return cmdWorker(ctx, *configPath, lookupEnv, stderr)
	case "migrate":
		return cmdMigrate(ctx, *configPath, fs.Args()[1:], lookupEnv, stdout, stderr)
	case "admin":
		return cmdAdmin(ctx, *configPath, fs.Args()[1:], lookupEnv, stdout, stderr)
	case "":
		_, _ = fmt.Fprintln(stderr, "usage: custos [--config path] <serve|worker|migrate|admin|version>")
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "unknown subcommand %q\n", fs.Arg(0))
		return 2
	}
}

// reportConfigError prints a config validation error with per-field
// details to stderr, for operators.
func reportConfigError(w io.Writer, err error) {
	var ae *apperr.Error
	if errors.As(err, &ae) && ae.Code == "config.invalid" {
		_, _ = fmt.Fprintf(w, "invalid configuration:\n")
		for _, d := range ae.Details {
			_, _ = fmt.Fprintf(w, "  %s: %s\n", d.Field, d.Reason)
		}
		return
	}
	_, _ = fmt.Fprintf(w, "config error: %v\n", err)
}
