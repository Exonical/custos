import "server-only";
import type { NextResponse } from "next/server";
import type { WebConfig } from "@/lib/config";
import { AUTH_SESSION_MAX_AGE, authSessionCookiePairs, replaceSessionCookieHeader } from "@/lib/session/request";
import { getCookieValue } from "@/lib/security/cookies";
import { isTenantSlug } from "@/lib/security/tenant-slug";

function requestCookieNames(request: Request, config: WebConfig): string[] {
  const prefix = config.cookieNames.session;
  return (request.headers.get("cookie") ?? "")
    .split(";")
    .map((part) => part.trim().split("=", 1)[0])
    .filter((name) => name === prefix || name.startsWith(`${prefix}.`));
}

function options(config: WebConfig, httpOnly: boolean, maxAge: number) {
  return {
    path: "/",
    secure: config.cookieSecure,
    httpOnly,
    sameSite: "lax" as const,
    maxAge,
  };
}

export function writeAuthSessionCookie(
  response: NextResponse,
  request: Request,
  value: string,
  config: WebConfig,
): void {
  const pairs = authSessionCookiePairs(value, config.cookieNames.session);
  const activeNames = new Set(pairs.map(([name]) => name));
  for (const name of new Set(requestCookieNames(request, config))) {
    if (!activeNames.has(name)) response.cookies.set(name, "", options(config, true, 0));
  }
  for (const [name, cookieValue] of pairs) {
    response.cookies.set(name, cookieValue, options(config, true, AUTH_SESSION_MAX_AGE));
  }
  response.headers.set("Cache-Control", "no-store");
}

export function clearAuthCookies(response: NextResponse, request: Request, config: WebConfig): void {
  const names = new Set([
    config.cookieNames.session,
    ...requestCookieNames(request, config),
    config.cookieNames.csrf,
    config.cookieNames.lastTenant,
    config.cookieNames.callbackUrl,
    config.cookieNames.authCsrf,
    config.cookieNames.oidcState,
    config.cookieNames.oidcNonce,
    config.cookieNames.oidcPkce,
  ]);
  for (const name of names) {
    const isBrowserReadable = name === config.cookieNames.csrf || name === config.cookieNames.lastTenant;
    response.cookies.set(name, "", options(config, !isBrowserReadable, 0));
  }
  response.headers.set("Cache-Control", "no-store");
}

export function readLastTenant(request: Request, config: WebConfig): string | null {
  const value = getCookieValue(request, config.cookieNames.lastTenant);
  return value && isTenantSlug(value) ? value : null;
}

export function setLastTenantCookie(response: NextResponse, tenant: string, config: WebConfig): void {
  if (!isTenantSlug(tenant)) return;
  response.cookies.set(config.cookieNames.lastTenant, tenant, options(config, false, AUTH_SESSION_MAX_AGE));
}

export function clearLastTenantCookie(response: NextResponse, config: WebConfig): void {
  response.cookies.set(config.cookieNames.lastTenant, "", options(config, false, 0));
}

export function forwardAuthSessionCookie(
  headers: Headers,
  request: Request,
  value: string,
  config: WebConfig,
): void {
  headers.set("cookie", replaceSessionCookieHeader(request, value, config.cookieNames.session));
}
