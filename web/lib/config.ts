import "server-only";
import { readFileSync } from "node:fs";
import { z } from "zod";

const envSchema = z.object({
  CUSTOS_WEB_ISSUER: z.url(),
  CUSTOS_WEB_CLIENT_ID: z.string().min(1),
  CUSTOS_WEB_CLIENT_SECRET_FILE: z.string().min(1),
  CUSTOS_WEB_PUBLIC_ORIGIN: z.url(),
  CUSTOS_API_URL: z.url(),
  CUSTOS_WEB_TRUSTED_PROXY_HOPS: z.preprocess(
    (value) => value === undefined ? "0" : value,
    z.string().regex(/^\d+$/).transform(Number).pipe(z.number().int().min(0)),
  ),
  CUSTOS_API_CA_FILE: z.string().min(1).optional(),
  CUSTOS_WEB_AUTH_SECRETS: z.string().min(1),
  CUSTOS_WEB_CA_FILE: z.string().min(1).optional(),
  CUSTOS_WEB_DEV: z.enum(["0", "1"]).optional(),
  CUSTOS_WEB_INSECURE_COOKIES: z.enum(["0", "1"]).optional(),
});

export type CookieNames = Readonly<{
  session: string;
  csrf: string;
  lastTenant: string;
  callbackUrl: string;
  authCsrf: string;
  oidcState: string;
  oidcNonce: string;
  oidcPkce: string;
}>;

export type WebConfig = Readonly<{
  issuer: URL;
  clientId: string;
  clientSecret: string;
  publicOrigin: string;
  apiUrl: URL;
  trustedProxyHops: number;
  apiCaFile?: string;
  authSecrets: readonly string[];
  webCaFile?: string;
  devMode: boolean;
  cookieSecure: boolean;
  cookieNames: CookieNames;
}>;

let cached: WebConfig | undefined;

function readFile(path: string, name: string): string {
  try {
    return readFileSync(path, "utf8").trim();
  } catch {
    throw new Error(`${name} could not be read`);
  }
}

export function parseAuthSecrets(value: string): string[] {
  const secrets = value.split(",").map((secret) => secret.trim());
  if (secrets.length === 0 || secrets.some((secret) => Buffer.byteLength(secret, "utf8") < 32)) {
    throw new Error("CUSTOS_WEB_AUTH_SECRETS entries must each be at least 32 bytes");
  }
  return secrets;
}

function parseURL(value: string, name: string, devMode: boolean): URL {
  const parsed = new URL(value);
  if (parsed.protocol !== "https:" && !devMode) {
    throw new Error(`${name} must use https unless CUSTOS_WEB_DEV=1`);
  }
  if (parsed.username || parsed.password || parsed.search || parsed.hash) {
    throw new Error(`${name} must not contain credentials, query, or fragment`);
  }
  return parsed;
}

export function getConfig(): WebConfig {
  if (cached) return cached;
  const env = envSchema.parse(process.env);
  const devMode = env.CUSTOS_WEB_DEV === "1";
  const insecureCookies = env.CUSTOS_WEB_INSECURE_COOKIES === "1";
  if (insecureCookies && process.env.NODE_ENV === "production") {
    throw new Error("CUSTOS_WEB_INSECURE_COOKIES=1 is not allowed in production");
  }

  const issuer = parseURL(env.CUSTOS_WEB_ISSUER, "CUSTOS_WEB_ISSUER", devMode);
  const publicURL = parseURL(env.CUSTOS_WEB_PUBLIC_ORIGIN, "CUSTOS_WEB_PUBLIC_ORIGIN", devMode);
  if (publicURL.pathname !== "/") {
    throw new Error("CUSTOS_WEB_PUBLIC_ORIGIN must be an origin without a path");
  }
  const apiUrl = parseURL(env.CUSTOS_API_URL, "CUSTOS_API_URL", devMode);
  if (apiUrl.pathname !== "/") {
    throw new Error("CUSTOS_API_URL must be an origin without a path");
  }

  const cookiePrefix = insecureCookies ? "" : "__Host-";
  const publicOrigin = publicURL.origin;
  process.env.AUTH_URL = publicOrigin;
  cached = Object.freeze({
    issuer,
    clientId: env.CUSTOS_WEB_CLIENT_ID,
    clientSecret: readFile(env.CUSTOS_WEB_CLIENT_SECRET_FILE, "CUSTOS_WEB_CLIENT_SECRET_FILE"),
    publicOrigin,
    apiUrl,
    trustedProxyHops: env.CUSTOS_WEB_TRUSTED_PROXY_HOPS,
    apiCaFile: env.CUSTOS_API_CA_FILE,
    authSecrets: Object.freeze(parseAuthSecrets(env.CUSTOS_WEB_AUTH_SECRETS)),
    webCaFile: env.CUSTOS_WEB_CA_FILE,
    devMode,
    cookieSecure: !insecureCookies,
    cookieNames: Object.freeze({
      session: `${cookiePrefix}custos_session`,
      csrf: `${cookiePrefix}custos_csrf`,
      lastTenant: `${cookiePrefix}custos_last_tenant`,
      callbackUrl: `${cookiePrefix}custos_auth_callback`,
      authCsrf: `${cookiePrefix}custos_auth_csrf`,
      oidcState: `${cookiePrefix}custos_oidc_state`,
      oidcNonce: `${cookiePrefix}custos_oidc_nonce`,
      oidcPkce: `${cookiePrefix}custos_oidc_pkce`,
    }),
  });
  return cached;
}

export function resetConfigForTest(): void {
  cached = undefined;
}
