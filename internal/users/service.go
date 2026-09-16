package users

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/authz"
	"github.com/Exonical/custos/internal/platform/apperr"
)

// Service implements authn.Provisioner: it maps a verified principal to
// a Custos user row, fills UserID/PlatformRoles, reconciles IdP claim
// memberships when due, and manages platform role bindings.
type Service struct {
	repo        Repository
	rec         audit.Recorder
	az          authz.Authorizer
	groupsClaim string
	logger      *slog.Logger
	syncCounter metric.Int64Counter
}

// Option customizes the service.
type Option func(*Service)

// WithAuthorizer enables service-level authz checks (platform role API).
func WithAuthorizer(az authz.Authorizer) Option {
	return func(s *Service) { s.az = az }
}

// WithGroupsClaim sets the JWT claim name whose values build the claims
// map used for rule matching (config auth.oidc.claims.groups).
func WithGroupsClaim(name string) Option {
	return func(s *Service) { s.groupsClaim = name }
}

// WithLogger sets the logger for reconciliation failures (availability
// over freshness: failures are logged, counted, and the request
// continues).
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) { s.logger = l }
}

// WithMeterProvider registers the custos_claims_sync_total counter.
func WithMeterProvider(mp metric.MeterProvider) Option {
	return func(s *Service) {
		if mp == nil {
			return
		}
		c, err := mp.Meter("custos").Int64Counter("custos_claims_sync_total")
		if err == nil {
			s.syncCounter = c
		}
	}
}

// NewService returns the provisioning service.
func NewService(repo Repository, rec audit.Recorder, opts ...Option) *Service {
	s := &Service{repo: repo, rec: rec, logger: slog.Default()}
	for _, o := range opts {
		o(s)
	}
	return s
}

func actorOf(p authn.Principal) audit.Actor {
	t := audit.ActorUser
	if p.Kind == authn.KindService {
		t = audit.ActorService
	}
	return audit.Actor{Type: t, ID: p.UserID.String()}
}

// Provision upserts the principal's (issuer, subject) identity, refreshes
// email/display name when they changed, throttles last_seen_at writes,
// loads platform role bindings, and reconciles idp claim memberships when
// the claims changed or the last sync is stale. On first sight it audits
// `user.provisioned` with the new user as actor.
func (s *Service) Provision(ctx context.Context, p authn.Principal) (authn.Principal, error) {
	u := User{
		Issuer:      p.Issuer,
		Subject:     p.Subject,
		Kind:        p.Kind,
		Email:       p.Email,
		DisplayName: p.Name,
	}
	u, created, err := s.repo.UpsertByIdentity(ctx, u)
	if err != nil {
		return authn.Principal{}, err
	}
	roles, err := s.repo.PlatformRoles(ctx, u.ID)
	if err != nil {
		return authn.Principal{}, err
	}
	p.UserID = u.ID
	p.PlatformRoles = roles
	if created && s.rec != nil {
		if err := s.rec.Record(ctx, audit.Event{
			Actor:   actorOf(p),
			Action:  "user.provisioned",
			Result:  audit.ResultAllow,
			Target:  audit.Target{Type: "user", ID: u.ID.String()},
			Details: map[string]any{"kind": string(p.Kind)},
		}); err != nil {
			return authn.Principal{}, apperr.Wrap(err, apperr.Unavailable,
				"users.provision", "cannot record provisioning audit")
		}
	}
	s.reconcile(ctx, u, p)
	return p, nil
}

// claimsSyncWindow is the freshness window for claim reconciliation.
const claimsSyncWindow = 5 * time.Minute

// claimsMap builds the claim-name → values map matched against rules.
// Today only the configured groups claim feeds it; the shape allows
// other claims later.
func (s *Service) claimsMap(p authn.Principal) map[string][]string {
	if s.groupsClaim == "" || len(p.Groups) == 0 {
		return nil
	}
	return map[string][]string{s.groupsClaim: p.Groups}
}

// claimsHash is a canonical sha256 over sorted claim names and values.
func claimsHash(claims map[string][]string) []byte {
	names := make([]string, 0, len(claims))
	for k := range claims {
		names = append(names, k)
	}
	slices.Sort(names)
	canon := make(map[string][]string, len(claims))
	for _, k := range names {
		v := slices.Clone(claims[k])
		slices.Sort(v)
		canon[k] = v
	}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return sum[:]
}

// reconcile runs claim sync when due. Failures are logged and counted —
// the request proceeds with whatever memberships exist.
func (s *Service) reconcile(ctx context.Context, u User, p authn.Principal) {
	claims := s.claimsMap(p)
	hash := claimsHash(claims)
	fresh := u.ClaimsSyncedAt != nil &&
		time.Since(*u.ClaimsSyncedAt) < claimsSyncWindow
	if fresh && slices.Equal(u.ClaimsHash, hash) {
		s.countSync(ctx, "skipped")
		return
	}
	out, err := s.repo.SyncIDPClaims(ctx, u.ID, claims, hash, time.Now())
	if err != nil {
		s.countSync(ctx, "error")
		s.logger.WarnContext(ctx, "claim reconciliation failed",
			"user_id", u.ID, "error", err)
		return
	}
	if out.Skipped {
		s.countSync(ctx, "skipped")
		return
	}
	s.countSync(ctx, "ok")
	if s.rec == nil {
		return
	}
	for _, ev := range out.Events {
		details := map[string]any{"source": "idp"}
		if len(ev.Roles) > 0 {
			details["roles"] = ev.Roles
		}
		if len(ev.RuleIDs) > 0 {
			details["rule_ids"] = ev.RuleIDs
		}
		if ev.Reason != "" {
			details["reason"] = ev.Reason
		}
		if ev.GroupID != uuid.Nil {
			details["group_id"] = ev.GroupID.String()
		}
		target := audit.Target{Type: "user", ID: ev.UserID.String()}
		if ev.GroupID != uuid.Nil {
			target.Type = "group"
			target.ID = ev.GroupID.String()
		}
		_ = s.rec.Record(ctx, audit.Event{
			Actor:    actorOf(p),
			Action:   ev.Action,
			Result:   audit.ResultAllow,
			Target:   target,
			TenantID: &ev.TenantID,
			Details:  details,
		})
	}
}

func (s *Service) countSync(ctx context.Context, result string) {
	if s.syncCounter != nil {
		s.syncCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("result", result)))
	}
}

// platformRes is the platform-scope authorization resource.
func platformRes() authz.Resource { return authz.Resource{Kind: "platform"} }

// ListRoleBindings returns platform role bindings with user identity
// (platform.manage or platform.audit.read).
func (s *Service) ListRoleBindings(ctx context.Context, p authn.Principal) ([]PlatformRoleBinding, error) {
	if err := authz.Require(ctx, s.az, p, authz.PlatformManage, platformRes(), s.rec); err != nil {
		if err2 := authz.Require(ctx, s.az, p, authz.PlatformAuditRead, platformRes(), s.rec); err2 != nil {
			return nil, err
		}
	}
	return s.repo.ListPlatformRoleBindings(ctx)
}

// GrantRole grants a platform role (platform.manage); the user must
// exist. Audits platform_role.granted with the caller as actor.
func (s *Service) GrantRole(ctx context.Context, p authn.Principal, userID uuid.UUID, role string) error {
	if err := authz.Require(ctx, s.az, p, authz.PlatformManage, platformRes(), s.rec); err != nil {
		return err
	}
	if !authz.ValidPlatformRole(role) {
		return apperr.New(apperr.Validation, "ROLE_INVALID", "unknown platform role")
	}
	return s.grantRole(ctx, actorOf(p), &p.UserID, userID, role)
}

// RevokeRole revokes a platform role (platform.manage); refuses to
// remove the last platform-admin. Audits platform_role.revoked.
func (s *Service) RevokeRole(ctx context.Context, p authn.Principal, userID uuid.UUID, role string) error {
	if err := authz.Require(ctx, s.az, p, authz.PlatformManage, platformRes(), s.rec); err != nil {
		return err
	}
	return s.revokeRole(ctx, actorOf(p), userID, role)
}

// GrantRoleSystem is the CLI path (actor custos-cli); no authz — the CLI
// is the bootstrap path.
func (s *Service) GrantRoleSystem(ctx context.Context, actor audit.Actor, userID uuid.UUID, role string) error {
	return s.grantRole(ctx, actor, nil, userID, role)
}

// RevokeRoleSystem is the CLI path (actor custos-cli).
func (s *Service) RevokeRoleSystem(ctx context.Context, actor audit.Actor, userID uuid.UUID, role string) error {
	return s.revokeRole(ctx, actor, userID, role)
}

func (s *Service) grantRole(ctx context.Context, actor audit.Actor, by *uuid.UUID, userID uuid.UUID, role string) error {
	if _, err := s.repo.GetByID(ctx, userID); err != nil {
		return err
	}
	if err := s.repo.GrantPlatformRole(ctx, userID, role, by); err != nil {
		return err
	}
	return s.record(ctx, actor, "platform_role.granted", userID,
		map[string]any{"role": role})
}

func (s *Service) revokeRole(ctx context.Context, actor audit.Actor, userID uuid.UUID, role string) error {
	if role == authz.RolePlatformAdmin {
		n, err := s.repo.CountPlatformRoleHolders(ctx, authz.RolePlatformAdmin)
		if err != nil {
			return err
		}
		if n <= 1 {
			return apperr.New(apperr.Conflict, "LAST_ADMIN",
				"cannot revoke the last platform-admin")
		}
	}
	if err := s.repo.RevokePlatformRole(ctx, userID, role); err != nil {
		return err
	}
	return s.record(ctx, actor, "platform_role.revoked", userID,
		map[string]any{"role": role})
}

func (s *Service) record(ctx context.Context, actor audit.Actor, action string, userID uuid.UUID, details map[string]any) error {
	if s.rec == nil {
		return nil
	}
	return s.rec.Record(ctx, audit.Event{
		Actor:   actor,
		Action:  action,
		Result:  audit.ResultAllow,
		Target:  audit.Target{Type: "user", ID: userID.String()},
		Details: details,
	})
}

// Ensure the service satisfies the provisioning port.
var _ authn.Provisioner = (*Service)(nil)
