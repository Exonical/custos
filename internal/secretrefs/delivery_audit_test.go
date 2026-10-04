package secretrefs

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/Exonical/custos/internal/audit"
)

type deliveryAudit struct {
	events []audit.Event
}

func (r *deliveryAudit) Record(_ context.Context, event audit.Event) error {
	r.events = append(r.events, event)
	return nil
}

func TestImagePullDeliveryAuditPurpose(t *testing.T) {
	recorder := &deliveryAudit{}
	service := &Service{audit: recorder}
	tenantID, referenceID, jobID := uuid.New(), uuid.New(), uuid.New()
	service.deliveryResult(context.Background(), DeliveryRequest{
		TenantID: tenantID, ReferenceID: referenceID, Mode: "image_pull", JobID: jobID,
	}, Reference{ID: referenceID}, "openbao", "allow")
	if len(recorder.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Action != "secret.accessed" || event.Details["purpose"] != "image_pull" ||
		event.Details["reference_id"] != referenceID.String() ||
		event.Details["job_id"] != jobID.String() {
		t.Fatalf("image pull delivery audit event = %+v", event)
	}
}
