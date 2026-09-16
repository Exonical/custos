package users

import (
	"context"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/apperr"
)

// Service implements authn.Provisioner: it maps a verified principal to
// a Custos user row and fills UserID/PlatformRoles on the principal.
type Service struct {
	repo Repository
	rec  audit.Recorder
}

// NewService returns the provisioning service.
func NewService(repo Repository, rec audit.Recorder) *Service {
	return &Service{repo: repo, rec: rec}
}

// Provision upserts the principal's (issuer, subject) identity, refreshes
// email/display name when they changed, throttles last_seen_at writes,
// and loads platform role bindings. On first sight it audits
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
		actorType := audit.ActorUser
		if p.Kind == authn.KindService {
			actorType = audit.ActorService
		}
		if err := s.rec.Record(ctx, audit.Event{
			Actor:    audit.Actor{Type: actorType, ID: u.ID.String()},
			Action:   "user.provisioned",
			Result:   audit.ResultAllow,
			Target:   audit.Target{Type: "user", ID: u.ID.String()},
			Details:  map[string]any{"kind": string(p.Kind)},
			TenantID: nil,
		}); err != nil {
			return authn.Principal{}, apperr.Wrap(err, apperr.Unavailable,
				"users.provision", "cannot record provisioning audit")
		}
	}
	return p, nil
}

// Ensure the service satisfies the provisioning port.
var _ authn.Provisioner = (*Service)(nil)
