import "server-only";
import { encode, getToken } from "next-auth/jwt";
import type { JWT } from "next-auth/jwt";
import { getConfig } from "@/lib/config";
import { jwtCallback } from "@/lib/auth/callbacks";
import { verifyCsrfToken } from "@/lib/security/csrf";
import { getCookieValue } from "@/lib/security/cookies";
import type { WebConfig } from "@/lib/config";
import type { RequestSession, RequestSessionResult } from "@/lib/session/model";

export const AUTH_SESSION_MAX_AGE = 8 * 60 * 60;
const AUTH_JS_COOKIE_CHUNK_SIZE = 4096 - 160;

export type RequestSessionOptions = Readonly<{
  refresh?: boolean;
  reseal?: boolean;
  forceRefreshAccessToken?: string;
}>;

function cookieEntries(request: Request): Array<[string, string]> {
  return (request.headers.get("cookie") ?? "")
    .split(";")
    .map((part) => part.trim())
    .filter(Boolean)
    .map((part) => {
      const separator = part.indexOf("=");
      return separator < 0 ? [part, ""] : [part.slice(0, separator), part.slice(separator + 1)];
    });
}

function hasSessionCookie(request: Request, cookieName: string): boolean {
  return cookieEntries(request).some(([name]) => name === cookieName || name.startsWith(`${cookieName}.`));
}

async function decodeAuthToken(request: Request, config: WebConfig): Promise<JWT | null> {
  const req = { headers: new Headers({ cookie: request.headers.get("cookie") ?? "" }) };
  return getToken({
    req,
    secret: [...config.authSecrets],
    salt: config.cookieNames.session,
    secureCookie: config.cookieSecure,
    cookieName: config.cookieNames.session,
  });
}

export function authSessionCookiePairs(value: string, cookieName: string): Array<[string, string]> {
  if (value.length <= AUTH_JS_COOKIE_CHUNK_SIZE) return [[cookieName, value]];
  const chunks: Array<[string, string]> = [];
  for (let offset = 0, index = 0; offset < value.length; offset += AUTH_JS_COOKIE_CHUNK_SIZE, index += 1) {
    chunks.push([`${cookieName}.${String(index)}`, value.slice(offset, offset + AUTH_JS_COOKIE_CHUNK_SIZE)]);
  }
  return chunks;
}

export function replaceSessionCookieHeader(request: Request, value: string, cookieName: string): string {
  const retained = cookieEntries(request).filter(([name]) => name !== cookieName && !name.startsWith(`${cookieName}.`));
  return [
    ...retained.map(([name, cookieValue]) => `${name}=${cookieValue}`),
    ...authSessionCookiePairs(value, cookieName).map(([name, cookieValue]) => `${name}=${cookieValue}`),
  ].join("; ");
}

async function encodeAuthToken(token: JWT, config: WebConfig): Promise<string> {
  return encode({
    token,
    secret: [...config.authSecrets],
    salt: config.cookieNames.session,
    maxAge: AUTH_SESSION_MAX_AGE,
  });
}

export async function getRequestSession(
  request: Request,
  options: RequestSessionOptions = {},
): Promise<RequestSessionResult> {
  const config = getConfig();
  const present = hasSessionCookie(request, config.cookieNames.session);
  let token: JWT | null;
  try {
    token = await decodeAuthToken(request, config);
  } catch {
    return { session: null, updatedCookie: null, clearSessionCookie: present };
  }
  if (!token || token.error || typeof token.sub !== "string" || typeof token.access_token !== "string") {
    return { session: null, updatedCookie: null, clearSessionCookie: present };
  }

  const forceRefresh = options.forceRefreshAccessToken !== undefined &&
    token.access_token === options.forceRefreshAccessToken;
  let current = token;
  let didRefresh = false;
  const expiresAt = typeof token.expires_at === "number" ? token.expires_at : 0;
  if (options.refresh !== false && (forceRefresh || expiresAt - Date.now() <= 60_000)) {
    current = await jwtCallback({ token, account: null }, { forceRefresh: forceRefresh || expiresAt - Date.now() <= 60_000 });
    if (current.error || typeof current.access_token !== "string" || typeof current.sub !== "string") {
      return { session: null, updatedCookie: null, clearSessionCookie: true };
    }
    didRefresh = true;
  }

  const subject = current.sub;
  const accessToken = current.access_token;
  if (typeof subject !== "string" || typeof accessToken !== "string") {
    return { session: null, updatedCookie: null, clearSessionCookie: true };
  }
  const secret = config.authSecrets[0];
  const csrfCookie = getCookieValue(request, config.cookieNames.csrf);
  const csrfToken = secret && csrfCookie && verifyCsrfToken(csrfCookie, subject, secret) ? csrfCookie : "";
  const session: RequestSession = {
    subject,
    accessToken,
    refreshToken: typeof current.refresh_token === "string" ? current.refresh_token : null,
    csrfToken,
  };
  const updatedCookie = didRefresh || options.reseal ? await encodeAuthToken(current, config) : null;
  return { session, updatedCookie, clearSessionCookie: false };
}

export async function requestAuthToken(request: Request): Promise<JWT | null> {
  const config = getConfig();
  return decodeAuthToken(request, config);
}

export function sessionCookiePresent(request: Request): boolean {
  const config = getConfig();
  return hasSessionCookie(request, config.cookieNames.session);
}
