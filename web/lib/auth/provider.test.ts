import { describe, expect, it } from "vitest";
import type { WebConfig } from "@/lib/config";
import { AUTH_BASE_PATH, createCustosOIDCProvider } from "./provider";

describe("Custos OIDC provider", () => {
  it("pins authorization and token exchange callbacks to the configured public origin", () => {
    const publicOrigin = "https://127.0.0.1:3000";
    const provider = createCustosOIDCProvider({
      issuer: new URL("https://keycloak.e2e:8443/realms/custos"),
      clientId: "custos-web",
      clientSecret: "test-client-secret",
      publicOrigin,
      webCaFile: undefined,
    } as WebConfig);

    expect(provider.redirectProxyUrl).toBe(`${publicOrigin}${AUTH_BASE_PATH}`);
  });
});
