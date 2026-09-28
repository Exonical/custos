import "server-only";
import * as oidc from "openid-client";
import type { Configuration, CustomFetch } from "openid-client";
import { getConfig } from "@/lib/config";
import { createSafeFetch } from "@/lib/http";

let configuration: Promise<Configuration> | undefined;

export function getOidcConfiguration(): Promise<Configuration> {
  configuration ??= loadConfiguration();
  return configuration;
}

async function loadConfiguration(): Promise<Configuration> {
  const config = getConfig();
  const safeFetch = createSafeFetch(config.webCaFile, 10_000);
  const customFetch: CustomFetch = (url, options) => safeFetch(url, {
    ...options,
    body: options.body as unknown as BodyInit,
  });
  const discovered = await oidc.discovery(
    config.issuer,
    config.clientId,
    { client_secret: config.clientSecret },
    oidc.ClientSecretPost(config.clientSecret),
    {
      [oidc.customFetch]: customFetch,
      timeout: 10,
      ...(config.devMode && config.issuer.protocol !== "https:"
        ? {
            // eslint-disable-next-line @typescript-eslint/no-deprecated -- Required only for explicit local HTTP development.
            execute: [oidc.allowInsecureRequests],
          }
        : {}),
    },
  );
  return discovered;
}

export async function refreshAccessToken(refreshToken: string) {
  const config = getConfig();
  const discovered = await getOidcConfiguration();
  if (config.devMode && config.issuer.protocol !== "https:") {
    // eslint-disable-next-line @typescript-eslint/no-deprecated -- Required only for explicit local HTTP development.
    oidc.allowInsecureRequests(discovered);
  }
  return oidc.refreshTokenGrant(discovered, refreshToken);
}

export async function createEndSessionUrl(): Promise<URL> {
  const config = getConfig();
  return oidc.buildEndSessionUrl(await getOidcConfiguration(), {
    client_id: config.clientId,
    post_logout_redirect_uri: `${config.publicOrigin}/signed-out`,
  });
}

export async function revokeRefreshToken(refreshToken: string | null): Promise<void> {
  if (!refreshToken) return;
  try {
    await oidc.tokenRevocation(await getOidcConfiguration(), refreshToken, {
      token_type_hint: "refresh_token",
    });
  } catch {
    // Revocation is best effort; the browser session is cleared regardless.
  }
}

export function resetOidcForTest(): void {
  configuration = undefined;
}
