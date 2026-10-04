import { customFetch } from "@auth/core";
import type { OIDCConfig } from "@auth/core/providers";
import type { Profile } from "next-auth";
import type { WebConfig } from "@/lib/config";
import { createSafeFetch } from "@/lib/http";

export const AUTH_BASE_PATH = "/api/auth";

export function createCustosOIDCProvider(config: WebConfig): OIDCConfig<Profile> {
  return {
    id: "custos",
    name: "Custos",
    type: "oidc",
    issuer: config.issuer.toString(),
    clientId: config.clientId,
    clientSecret: config.clientSecret,
    redirectProxyUrl: `${config.publicOrigin}${AUTH_BASE_PATH}`,
    checks: ["pkce", "state", "nonce"],
    authorization: { params: { scope: "openid profile email" } },
    [customFetch]: createSafeFetch(config.webCaFile, 10_000),
  };
}
