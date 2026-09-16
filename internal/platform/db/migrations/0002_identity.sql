-- +goose Up
-- Identity schema (M2): platform-level users and role bindings,
-- tenant table, and tenant memberships. RLS on tenant-owned tables is
-- defense-in-depth behind the tenant.Scope repository contract
-- (docs/tenancy.md); app.tenant_id is a tenant uuid or '*' for platform
-- scope. Unset -> zero rows.
-- +goose StatementBegin
CREATE TABLE users (
    id           uuid PRIMARY KEY,
    issuer       text NOT NULL,
    subject      text NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('user','service')),
    email        text,
    display_name text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz,
    UNIQUE (issuer, subject)
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX users_email ON users (lower(email));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE platform_role_bindings (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('platform-admin','platform-auditor')),
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES users(id),
    PRIMARY KEY (user_id, role)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE tenants (
    id                 uuid PRIMARY KEY,
    slug               text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    name               text NOT NULL,
    state              text NOT NULL CHECK (state IN ('provisioning','active','suspended','deleting','deleted')),
    settings           jsonb NOT NULL DEFAULT '{}'::jsonb,
    openbao_namespace  text,
    version            integer NOT NULL DEFAULT 1,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE tenant_memberships (
    tenant_id  uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    roles      text[] NOT NULL CHECK (cardinality(roles) > 0),
    source     text NOT NULL CHECK (source IN ('manual','idp')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX tenant_memberships_user ON tenant_memberships (user_id);
-- +goose StatementEnd

-- FORCE means even the table owner is subject to the policies; only a
-- superuser (or BYPASSRLS role) escapes, which is why runtime paths use
-- the least-privilege app role.
-- +goose StatementBegin
ALTER TABLE tenant_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_memberships FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_memberships_scope ON tenant_memberships
    USING (current_setting('app.tenant_id', true) = '*'
           OR tenant_id::text = current_setting('app.tenant_id', true))
    WITH CHECK (current_setting('app.tenant_id', true) = '*'
                OR tenant_id::text = current_setting('app.tenant_id', true));
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenants_scope ON tenants
    USING (current_setting('app.tenant_id', true) = '*'
           OR id::text = current_setting('app.tenant_id', true))
    WITH CHECK (current_setting('app.tenant_id', true) = '*');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS tenant_memberships;
DROP TABLE IF EXISTS tenants;
DROP TABLE IF EXISTS platform_role_bindings;
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
