-- +goose Up
CREATE TABLE cluster_node_config (
  cluster_id uuid PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
  config jsonb NOT NULL CHECK (jsonb_typeof(config) = 'object'),
  revision bigint NOT NULL CHECK (revision >= 1),
  content_sha256 text NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid REFERENCES users(id)
);

CREATE TABLE cluster_node_tokens (
  id uuid PRIMARY KEY,
  cluster_id uuid NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
  token_sha256 text NOT NULL UNIQUE CHECK (token_sha256 ~ '^[0-9a-f]{64}$'),
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid REFERENCES users(id),
  last_used_at timestamptz,
  revoked_at timestamptz
);
CREATE INDEX cluster_node_tokens_cluster ON cluster_node_tokens (cluster_id, created_at);

CREATE TABLE cluster_node_status (
  cluster_id uuid NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  node_name text NOT NULL CHECK (node_name ~ '^[A-Za-z0-9._-]{1,128}$'),
  token_id uuid REFERENCES cluster_node_tokens(id) ON DELETE SET NULL,
  revision bigint NOT NULL,
  bundle_sha256 text NOT NULL CHECK (bundle_sha256 ~ '^[0-9a-f]{64}$'),
  fetched_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (cluster_id, node_name)
);

-- +goose Down
DROP TABLE cluster_node_status;
DROP TABLE cluster_node_tokens;
DROP TABLE cluster_node_config;
