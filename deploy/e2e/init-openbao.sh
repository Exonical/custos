#!/bin/sh
set -eu
export BAO_ADDR=https://openbao:8200
export BAO_CACERT=/openbao/tls/ca.crt
out=/bootstrap
until bao status >/dev/null 2>&1 || [ "$?" -eq 2 ]; do sleep 1; done
if bao status -format=json 2>/dev/null | grep -q '"initialized": false'; then
	init=$(bao operator init -key-shares=1 -key-threshold=1)
	printf '%s\n' "$init" | sed -n 's/^Unseal Key 1: //p' > "$out/openbao-unseal-key"
	printf '%s\n' "$init" | sed -n 's/^Initial Root Token: //p' > "$out/openbao-root-token"
	chmod 600 "$out/openbao-unseal-key" "$out/openbao-root-token" 2>/dev/null || true
	rm -f "$out/openbao-bootstrapped"
fi
bao operator unseal "$(cat "$out/openbao-unseal-key")" >/dev/null 2>&1 || true
[ -f "$out/openbao-bootstrapped" ] && exit 0
export BAO_TOKEN=$(cat "$out/openbao-root-token")
bao namespace create custos >/dev/null 2>&1 || true
export BAO_NAMESPACE=custos
bao secrets enable -path=kv kv-v2 >/dev/null 2>&1 || true
bao auth enable jwt >/dev/null 2>&1 || true
bao write auth/jwt/config \
	jwks_url=https://keycloak.e2e:8443/realms/custos/protocol/openid-connect/certs \
	jwks_ca_pem=@/openbao/tls/ca.crt \
	jwt_supported_algs=RS256 >/dev/null
cat > /tmp/custos-policy.hcl <<'EOF'
path "kv/data/clusters/*" { capabilities = ["read"] }
path "sys/namespaces/tenants" { capabilities = ["create", "update", "read"] }
path "tenants/sys/namespaces/*" { capabilities = ["create", "update", "read"] }
path "tenants/+/sys/mounts/kv" { capabilities = ["create", "update", "read"] }
path "tenants/+/sys/policies/acl/tenant-*" { capabilities = ["create", "update", "read"] }
path "tenants/+/sys/policies/acl/ref-*" { capabilities = ["create", "update", "read"] }
path "tenants/+/auth/token/create-orphan" { capabilities = ["create", "update", "sudo"] }
path "tenants/+/kv/data/connectors/*" { capabilities = ["create", "update", "read", "delete"] }
EOF
bao policy write custos /tmp/custos-policy.hcl >/dev/null
bao write auth/jwt/role/custos role_type=jwt user_claim=sub \
	bound_audiences=custos-openbao token_policies=custos \
	token_ttl=5m token_max_ttl=15m >/dev/null
cat > /tmp/bootstrap-policy.hcl <<'EOF'
path "kv/data/clusters/*" { capabilities = ["create", "update", "read"] }
path "tenants/+/kv/data/users/*" { capabilities = ["create", "update", "read"] }
path "sys/internal/ui/mounts/*" { capabilities = ["read"] }
path "tenants/+/sys/internal/ui/mounts/*" { capabilities = ["read"] }
EOF
bao policy write e2e-bootstrap /tmp/bootstrap-policy.hcl >/dev/null
bao token create -field=token -orphan -policy=e2e-bootstrap -ttl=2h > "$out/openbao-bootstrap-token"
chmod 600 "$out/openbao-bootstrap-token" 2>/dev/null || true
bao token revoke -self >/dev/null
touch "$out/openbao-bootstrapped"
