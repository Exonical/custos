// Package pgaudit is the PostgreSQL audit sink: events are appended to a
// per-stream hash chain (audit_streams + audit_events) inside one
// transaction so sequence numbers and hashes are strictly ordered.
package pgaudit

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Exonical/custos/internal/audit"
)

// Sink records events into the PostgreSQL hash chain.
type Sink struct {
	pool *pgxpool.Pool
}

// New returns a Sink on pool.
func New(pool *pgxpool.Pool) *Sink { return &Sink{pool: pool} }

// canonical is the fixed-field-order JSON representation hashed into the
// chain. Details marshal via encoding/json, which sorts map keys, so the
// encoding is deterministic. Note: Details are round-tripped through
// json.Unmarshal before hashing so the hashed shape matches what jsonb
// stores; residual jsonb normalizations we cannot observe at Record time
// (e.g. numeric-format quirks like 1.50 -> 1.5 in jsonb) are out of
// scope — keep Details to simple scalars/maps/slices.
type canonical struct {
	ID           string         `json:"id"`
	OccurredAt   string         `json:"occurred_at"`
	Stream       string         `json:"stream"`
	Seq          int64          `json:"seq"`
	TenantID     *string        `json:"tenant_id"`
	ActorType    string         `json:"actor_type"`
	ActorID      string         `json:"actor_id"`
	ActorDisplay string         `json:"actor_display"`
	Action       string         `json:"action"`
	TargetType   string         `json:"target_type"`
	TargetID     string         `json:"target_id"`
	Result       string         `json:"result"`
	Reason       string         `json:"reason"`
	RequestID    string         `json:"request_id"`
	TraceID      string         `json:"trace_id"`
	ClientIP     string         `json:"client_ip"`
	Details      map[string]any `json:"details"`
}

func canon(e audit.Event, seq int64) ([]byte, error) {
	var tid *string
	if e.TenantID != nil {
		s := e.TenantID.String()
		tid = &s
	}
	return json.Marshal(canonical{
		ID: e.ID.String(),
		// timestamptz stores microseconds; truncate so the hash input
		// matches what Verify reads back.
		OccurredAt:   e.OccurredAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano),
		Stream:       e.Stream(),
		Seq:          seq,
		TenantID:     tid,
		ActorType:    e.Actor.Type,
		ActorID:      e.Actor.ID,
		ActorDisplay: e.Actor.Display,
		Action:       e.Action,
		TargetType:   e.Target.Type,
		TargetID:     e.Target.ID,
		Result:       e.Result,
		Reason:       e.Reason,
		RequestID:    e.RequestID,
		TraceID:      e.TraceID,
		ClientIP:     e.ClientIP,
		Details:      e.Details,
	})
}

func hash(prev, body []byte) []byte {
	h := sha256.New()
	h.Write(prev)
	h.Write(body)
	return h.Sum(nil)
}

// Record appends e to its stream's hash chain in one transaction.
// Zero ID/OccurredAt are filled (UUIDv7 / now UTC).
func (s *Sink) Record(ctx context.Context, e audit.Event) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.Must(uuid.NewV7())
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	// Normalize Details through a JSON round-trip so the hash input
	// matches what Verify reads back from jsonb.
	djson, err := json.Marshal(e.Details)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(djson, &e.Details); err != nil {
		return err
	}
	stream := e.Stream()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		"INSERT INTO audit_streams(stream) VALUES($1) ON CONFLICT DO NOTHING",
		stream); err != nil {
		return err
	}
	var seq int64
	var prevHash []byte
	if err := tx.QueryRow(ctx,
		"SELECT seq, last_hash FROM audit_streams WHERE stream=$1 FOR UPDATE",
		stream).Scan(&seq, &prevHash); err != nil {
		return err
	}
	seq++

	body, err := canon(e, seq)
	if err != nil {
		return err
	}
	h := hash(prevHash, body)

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_events
		  (id, occurred_at, stream, seq, tenant_id, actor_type, actor_id,
		   actor_display, action, target_type, target_id, result, reason,
		   request_id, trace_id, client_ip, details, prev_hash, hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		e.ID, e.OccurredAt, stream, seq, e.TenantID, e.Actor.Type, e.Actor.ID,
		nilIfEmpty(e.Actor.Display), e.Action, nilIfEmpty(e.Target.Type),
		nilIfEmpty(e.Target.ID), e.Result, nilIfEmpty(e.Reason),
		nilIfEmpty(e.RequestID), nilIfEmpty(e.TraceID), nilIfEmpty(e.ClientIP),
		djson, prevHash, h); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		"UPDATE audit_streams SET seq=$2, last_hash=$3 WHERE stream=$1",
		stream, seq, h); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Verify recomputes the chain for stream and returns the number of events
// verified. On a break it returns the seq of the first bad row and a
// non-nil error.
func Verify(ctx context.Context, pool *pgxpool.Pool, stream string) (int, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, occurred_at, seq, tenant_id, actor_type, actor_id,
		       coalesce(actor_display,''), action, coalesce(target_type,''),
		       coalesce(target_id,''), result, coalesce(reason,''),
		       coalesce(request_id,''), coalesce(trace_id,''),
		       coalesce(client_ip,''), details, prev_hash, hash
		FROM audit_events WHERE stream=$1 ORDER BY seq`, stream)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var prev []byte
	n := 0
	for rows.Next() {
		var e audit.Event
		var seq int64
		var details []byte
		var prevHash, h []byte
		if err := rows.Scan(&e.ID, &e.OccurredAt, &seq, &e.TenantID,
			&e.Actor.Type, &e.Actor.ID, &e.Actor.Display, &e.Action,
			&e.Target.Type, &e.Target.ID, &e.Result, &e.Reason,
			&e.RequestID, &e.TraceID, &e.ClientIP, &details,
			&prevHash, &h); err != nil {
			return n, err
		}
		if err := json.Unmarshal(details, &e.Details); err != nil {
			return n, err
		}
		if string(prevHash) != string(prev) {
			return n, &ChainError{Seq: seq, Msg: "prev_hash mismatch"}
		}
		body, err := canon(e, seq)
		if err != nil {
			return n, err
		}
		if got := hash(prev, body); string(got) != string(h) {
			return n, &ChainError{Seq: seq, Msg: "hash mismatch"}
		}
		prev = h
		n++
	}
	return n, rows.Err()
}

// ChainError reports a hash-chain break at Seq.
type ChainError struct {
	Seq int64
	Msg string
}

// Error implements error.
func (c *ChainError) Error() string {
	return "audit chain broken at seq " + strconv.FormatInt(c.Seq, 10) + ": " + c.Msg
}
