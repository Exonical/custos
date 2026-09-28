import "server-only";
import { customFetch } from "@auth/core";
import type { OIDCConfig } from "@auth/core/providers";
import NextAuth, { type NextAuthConfig, type Profile } from "next-auth";
import { getConfig } from "@/lib/config";
import { createSafeFetch } from "@/lib/http";
import { jwtCallback, sessionCallback } from "@/lib/auth/callbacks";

export function buildAuthConfig(): NextAuthConfig {
  const config = getConfig();
  process.env.AUTH_URL = config.publicOrigin;
  const cookieOptions = {
    path: "/",
    secure: config.cookieSecure,
    sameSite: "lax" as const,
  };
  const provider: OIDCConfig<Profile> = {
    id: "custos",
    name: "Custos",
    type: "oidc",
    issuer: config.issuer.toString(),
    clientId: config.clientId,
    clientSecret: config.clientSecret,
    checks: ["pkce", "state", "nonce"],
    authorization: { params: { scope: "openid profile email" } },
    [customFetch]: createSafeFetch(config.webCaFile, 10_000),
  };

  return {
    secret: [...config.authSecrets],
    trustHost: true,
    basePath: "/api/auth",
    useSecureCookies: config.cookieSecure,
    session: { strategy: "jwt", maxAge: 8 * 60 * 60 },
    pages: { signIn: "/auth/login", error: "/auth/error" },
    cookies: {
      sessionToken: {
        name: config.cookieNames.session,
        options: { ...cookieOptions, httpOnly: true },
      },
      callbackUrl: {
        name: config.cookieNames.callbackUrl,
        options: { ...cookieOptions, httpOnly: true },
      },
      csrfToken: {
        name: config.cookieNames.authCsrf,
        options: { ...cookieOptions, httpOnly: true },
      },
      state: {
        name: config.cookieNames.oidcState,
        options: { ...cookieOptions, httpOnly: true, maxAge: 15 * 60 },
      },
      nonce: {
        name: config.cookieNames.oidcNonce,
        options: { ...cookieOptions, httpOnly: true, maxAge: 15 * 60 },
      },
      pkceCodeVerifier: {
        name: config.cookieNames.oidcPkce,
        options: { ...cookieOptions, httpOnly: true, maxAge: 15 * 60 },
      },
    },
    providers: [provider],
    callbacks: {
      jwt: async ({ token, account, profile }) => jwtCallback({ token, account: account ?? null, profile }),
      session: ({ session, token }) => sessionCallback(session, token),
    },
  };
}

export const { handlers, auth, signIn, signOut } = NextAuth(buildAuthConfig);
