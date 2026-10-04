import "server-only";
import NextAuth, { type NextAuthConfig } from "next-auth";
import { getConfig } from "@/lib/config";
import { jwtCallback, sessionCallback } from "@/lib/auth/callbacks";
import { AUTH_BASE_PATH, createCustosOIDCProvider } from "@/lib/auth/provider";

export function buildAuthConfig(): NextAuthConfig {
  const config = getConfig();
  process.env.AUTH_URL = config.publicOrigin;
  const cookieOptions = {
    path: "/",
    secure: config.cookieSecure,
    sameSite: "lax" as const,
  };
  const provider = createCustosOIDCProvider(config);

  return {
    secret: [...config.authSecrets],
    trustHost: true,
    basePath: AUTH_BASE_PATH,
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
