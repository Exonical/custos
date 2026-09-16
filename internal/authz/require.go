package authz

import (
	"context"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
	"github.com/Exonical/custos/internal/authn"
	"github.com/Exonical/custos/internal/platform/apperr"
)

// Require checks (p, action, res) with az, audits denies as
// `authz.denied`, and returns nil on allow or apperr Forbidden
// FORBIDDEN on deny. Services may map that to NotFound when resource
// existence must be hidden. Infrastructure errors deny.
func Require(ctx context.Context, az Authorizer, p authn.Principal,
	action Action, res Resource, rec audit.Recorder) error {
	d, err := az.Check(ctx, p, action, res)
	reason := d.Reason
	if err != nil {
		d.Allow = false
		reason = "authorizer error"
	}
	if d.Allow {
		return nil
	}
	if rec != nil {
		actorType := audit.ActorUser
		if p.Kind == authn.KindService {
			actorType = audit.ActorService
		}
		var tenantID *uuid.UUID
		if id, perr := uuid.Parse(res.TenantID); perr == nil {
			tenantID = &id
		}
		if rerr := rec.Record(ctx, audit.Event{
			Actor:    audit.Actor{Type: actorType, ID: p.UserID.String()},
			Action:   "authz.denied",
			Target:   audit.Target{Type: res.Kind, ID: res.ID},
			Result:   audit.ResultDeny,
			Reason:   reason,
			TenantID: tenantID,
			Details:  map[string]any{"action": string(action)},
		}); rerr != nil {
			// An audit failure must not turn a deny into an allow; the
			// deny stands either way.
			_ = rerr
		}
	}
	return apperr.New(apperr.Forbidden, "FORBIDDEN", "forbidden")
}
