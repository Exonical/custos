package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/audit/pgaudit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/config"
	"github.com/Exonical/custos/internal/platform/db"
	"github.com/Exonical/custos/internal/platform/log"
	"github.com/Exonical/custos/internal/users"
	userpg "github.com/Exonical/custos/internal/users/postgres"
)

var cliActor = audit.Actor{Type: audit.ActorSystem, ID: "custos-cli"}

// cmdAdmin implements `custos admin platform-role grant|revoke|list` —
// the bootstrap path that creates the first platform-admin before any
// tenant exists.
func cmdAdmin(ctx context.Context, configPath string, args []string, lookupEnv config.LookupEnv, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "platform-role" {
		_, _ = fmt.Fprintln(stderr,
			"usage: custos admin platform-role <grant|revoke|list> [--issuer iss --subject sub --role r]")
		return 2
	}
	fs := flag.NewFlagSet("admin platform-role", flag.ContinueOnError)
	fs.SetOutput(stderr)
	issuer := fs.String("issuer", "", "OIDC issuer")
	subject := fs.String("subject", "", "OIDC subject")
	role := fs.String("role", "", "platform-admin | platform-auditor")
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	op := args[1]
	if op != "grant" && op != "revoke" && op != "list" {
		_, _ = fmt.Fprintf(stderr, "unknown platform-role command %q\n", op)
		return 2
	}
	if op != "list" && (*issuer == "" || *subject == "" || *role == "") {
		_, _ = fmt.Fprintln(stderr, "grant/revoke require --issuer, --subject, --role")
		return 2
	}
	if op != "list" && !authz.ValidPlatformRole(*role) {
		_, _ = fmt.Fprintf(stderr, "invalid role %q\n", *role)
		return 2
	}

	cfg, err := config.Load(configPath, lookupEnv)
	if err != nil {
		reportConfigError(stderr, err)
		return 1
	}
	logger, lerr := log.New(cfg.Log, stderr)
	if lerr != nil {
		_, _ = fmt.Fprintf(stderr, "log setup: %v\n", lerr)
		return 1
	}
	pool, err := db.Open(ctx, cfg.Database, logger)
	if err != nil {
		logger.ErrorContext(ctx, "database unavailable", "error", err)
		return 1
	}
	defer pool.Close()

	repo := userpg.New(pool)
	rec := audit.Multi{pgaudit.New(pool), audit.SlogRecorder{Logger: logger}}
	svc := users.NewService(repo, rec)

	switch op {
	case "list":
		bs, err := repo.ListPlatformRoleBindings(ctx)
		if err != nil {
			logger.ErrorContext(ctx, "list bindings", "error", err)
			return 1
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "USER_ID\tROLE\tCREATED_AT")
		for _, b := range bs {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n",
				b.UserID, b.Role, b.CreatedAt.Format("2006-01-02T15:04:05Z"))
		}
		return exitErr(tw.Flush())
	case "grant":
		u, _, err := repo.UpsertByIdentity(ctx, users.User{
			Issuer: *issuer, Subject: *subject, Kind: authn.KindUser,
		})
		if err != nil {
			logger.ErrorContext(ctx, "ensure user", "error", err)
			return 1
		}
		if err := svc.GrantRoleSystem(ctx, cliActor, u.ID, *role); err != nil {
			logger.ErrorContext(ctx, "grant role", "error", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "granted %s to user %s\n", *role, u.ID)
		return 0
	case "revoke":
		u, _, err := repo.UpsertByIdentity(ctx, users.User{
			Issuer: *issuer, Subject: *subject, Kind: authn.KindUser,
		})
		if err != nil {
			logger.ErrorContext(ctx, "ensure user", "error", err)
			return 1
		}
		if err := svc.RevokeRoleSystem(ctx, cliActor, u.ID, *role); err != nil {
			logger.ErrorContext(ctx, "revoke role", "error", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "revoked %s from user %s\n", *role, u.ID)
		return 0
	}
	return 2
}

func exitErr(err error) int {
	if err != nil {
		return 1
	}
	return 0
}
