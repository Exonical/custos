-- +goose Up
-- pgcrypto: required baseline extension (ADR-013); asserted by preflight,
-- used for server-side digest/random helpers as they are introduced.
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pgcrypto;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE work_items (
    id            uuid PRIMARY KEY,
    kind          text NOT NULL,
    key           text NOT NULL,
    tenant_id     uuid,
    payload       jsonb NOT NULL DEFAULT '{}'::jsonb,
    run_at        timestamptz NOT NULL,
    priority      smallint NOT NULL DEFAULT 0,
    attempt       integer NOT NULL DEFAULT 0,
    max_attempts  integer NOT NULL,
    leased_until  timestamptz,
    leased_by     text,
    state         text NOT NULL CHECK (state IN ('pending','leased','done','dead')),
    last_error    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE UNIQUE INDEX work_items_active_key ON work_items (kind, key) WHERE state IN ('pending','leased');
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX work_items_ready ON work_items (kind, run_at, priority DESC) WHERE state = 'pending';
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX work_items_leased ON work_items (kind, leased_until) WHERE state = 'leased';
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX work_items_finished ON work_items (finished_at) WHERE state IN ('done','dead');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE idempotency_keys (
    tenant_id       uuid NOT NULL,        -- FK to tenants added in M2 when the table exists
    key             text NOT NULL,
    request_hash    bytea NOT NULL,
    response_status integer NOT NULL,
    response_body   bytea,
    resource_id     uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, key)
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX idempotency_keys_expires ON idempotency_keys (expires_at);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE audit_streams (
    stream     text PRIMARY KEY,           -- 'platform' or 'tenant:<uuid>'
    seq        bigint NOT NULL DEFAULT 0,
    last_hash  bytea NOT NULL DEFAULT ''::bytea
);
-- +goose StatementEnd

-- Append-only: the app role gets no UPDATE/DELETE on audit_events; grants
-- are a deployment concern handled in M2.
-- +goose StatementBegin
CREATE TABLE audit_events (
    id           uuid NOT NULL,
    occurred_at  timestamptz NOT NULL,
    stream       text NOT NULL,
    seq          bigint NOT NULL,
    tenant_id    uuid,
    actor_type   text NOT NULL,            -- user | service | system
    actor_id     text NOT NULL,
    actor_display text,
    action       text NOT NULL,            -- e.g. system.started
    target_type  text,
    target_id    text,
    result       text NOT NULL CHECK (result IN ('allow','deny','error')),
    reason       text,
    request_id   text,
    trace_id     text,
    client_ip    text,
    details      jsonb NOT NULL DEFAULT '{}'::jsonb,
    prev_hash    bytea NOT NULL,
    hash         bytea NOT NULL,
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX audit_events_stream_seq ON audit_events (stream, seq);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX audit_events_tenant_time ON audit_events (tenant_id, occurred_at DESC);
-- +goose StatementEnd

-- Append-only enforcement independent of role grants (ADR-013): row
-- triggers on a partitioned parent propagate to partitions.
-- +goose StatementBegin
CREATE FUNCTION audit_events_immutable() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER audit_events_immutable_row
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_immutable();
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER audit_events_immutable_truncate
    BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_events_immutable();
-- +goose StatementEnd

-- Monthly partitions covering 2026-01 .. 2027-12; the
-- maintenance.partitions work item keeps the range ahead after that.
-- +goose StatementBegin
CREATE TABLE audit_events_202601 PARTITION OF audit_events FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');
CREATE TABLE audit_events_202602 PARTITION OF audit_events FOR VALUES FROM ('2026-02-01') TO ('2026-03-01');
CREATE TABLE audit_events_202603 PARTITION OF audit_events FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE audit_events_202604 PARTITION OF audit_events FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE audit_events_202605 PARTITION OF audit_events FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE audit_events_202606 PARTITION OF audit_events FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE audit_events_202607 PARTITION OF audit_events FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE audit_events_202608 PARTITION OF audit_events FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE audit_events_202609 PARTITION OF audit_events FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE audit_events_202610 PARTITION OF audit_events FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
CREATE TABLE audit_events_202611 PARTITION OF audit_events FOR VALUES FROM ('2026-11-01') TO ('2026-12-01');
CREATE TABLE audit_events_202612 PARTITION OF audit_events FOR VALUES FROM ('2026-12-01') TO ('2027-01-01');
CREATE TABLE audit_events_202701 PARTITION OF audit_events FOR VALUES FROM ('2027-01-01') TO ('2027-02-01');
CREATE TABLE audit_events_202702 PARTITION OF audit_events FOR VALUES FROM ('2027-02-01') TO ('2027-03-01');
CREATE TABLE audit_events_202703 PARTITION OF audit_events FOR VALUES FROM ('2027-03-01') TO ('2027-04-01');
CREATE TABLE audit_events_202704 PARTITION OF audit_events FOR VALUES FROM ('2027-04-01') TO ('2027-05-01');
CREATE TABLE audit_events_202705 PARTITION OF audit_events FOR VALUES FROM ('2027-05-01') TO ('2027-06-01');
CREATE TABLE audit_events_202706 PARTITION OF audit_events FOR VALUES FROM ('2027-06-01') TO ('2027-07-01');
CREATE TABLE audit_events_202707 PARTITION OF audit_events FOR VALUES FROM ('2027-07-01') TO ('2027-08-01');
CREATE TABLE audit_events_202708 PARTITION OF audit_events FOR VALUES FROM ('2027-08-01') TO ('2027-09-01');
CREATE TABLE audit_events_202709 PARTITION OF audit_events FOR VALUES FROM ('2027-09-01') TO ('2027-10-01');
CREATE TABLE audit_events_202710 PARTITION OF audit_events FOR VALUES FROM ('2027-10-01') TO ('2027-11-01');
CREATE TABLE audit_events_202711 PARTITION OF audit_events FOR VALUES FROM ('2027-11-01') TO ('2027-12-01');
CREATE TABLE audit_events_202712 PARTITION OF audit_events FOR VALUES FROM ('2027-12-01') TO ('2028-01-01');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS audit_events;
DROP FUNCTION IF EXISTS audit_events_immutable();
DROP TABLE IF EXISTS audit_streams;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS work_items;
-- +goose StatementEnd
