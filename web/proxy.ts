import { randomBytes, randomUUID } from "node:crypto";
import { NextRequest, NextResponse, type NextFetchEvent, type NextProxy } from "next/server";
import { auth } from "@/auth";
import type { NextAuthRequest } from "next-auth";
import { getConfig } from "@/lib/config";
import { isUsableAuthState } from "@/lib/auth/callbacks";
import { createCsrfToken, setCsrfCookie } from "@/lib/security/csrf";
import { isTenantSlug } from "@/lib/security/tenant-slug";
import { requestHostMatchesOrigin } from "@/lib/security/host";
import { clearAuthCookies, setLastTenantCookie } from "@/lib/session/auth-cookie";
import { getRequestSession, replaceSessionCookieHeader, requestAuthToken } from "@/lib/session/request";

function csp(nonce: string, development: boolean): string {
  return [
    "default-src 'self'",
    `script-src 'self' 'nonce-${nonce}' 'strict-dynamic'${development ? " 'unsafe-eval'" : ""}`,
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data:",
    "connect-src 'self'",
    "frame-ancestors 'none'",
    "base-uri 'none'",
    "form-action 'self'",
  ].join("; ");
}

function secureHeaders(response: NextResponse, policy: string, devMode: boolean): NextResponse {
  response.headers.set("Content-Security-Policy", policy);
  response.headers.set("X-Content-Type-Options", "nosniff");
  response.headers.set("Referrer-Policy", "strict-origin-when-cross-origin");
  response.headers.set("Permissions-Policy", "camera=(), microphone=(), geolocation=()");
  if (!devMode) response.headers.set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload");
  return response;
}

function loginRedirect(request: NextRequest, publicOrigin: string): NextResponse {
  const returnTo = `${request.nextUrl.pathname}${request.nextUrl.search}`;
  const url = new URL("/auth/login", publicOrigin);
  url.searchParams.set("returnTo", returnTo);
  return NextResponse.redirect(url);
}

function unauthorized(): NextResponse {
  return NextResponse.json(
    { error: { code: "UNAUTHENTICATED", request_id: randomUUID() } },
    { status: 401, headers: { "Cache-Control": "no-store" } },
  );
}

function replaceCookie(header: string, name: string, value: string): string {
  const retained = header.split(";").map((part) => part.trim()).filter(Boolean)
    .filter((part) => part.slice(0, part.indexOf("=")).trim() !== name);
  return [...retained, `${name}=${value}`].join("; ");
}

function toNextResponse(response: Response): NextResponse {
  const setCookies = response.headers.getSetCookie();
  const headers = new Headers(response.headers);
  headers.delete("set-cookie");
  const nextResponse = new NextResponse(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
  for (const cookie of setCookies) nextResponse.headers.append("Set-Cookie", cookie);
  return nextResponse;
}

const authCallback: (request: NextAuthRequest, event: NextFetchEvent) => Promise<Response> = async (request) => {
  const config = getConfig();
  const token = await requestAuthToken(request);
  const path = request.nextUrl.pathname;
  const protectedPage = path === "/" || path === "/select-tenant" || path.startsWith("/t/");
  if (protectedPage && !isUsableAuthState(request.auth, token)) {
    const response = loginRedirect(request, config.publicOrigin);
    clearAuthCookies(response, request, config);
    return response;
  }
  return NextResponse.next({ request: { headers: new Headers(request.headers) } });
};

let authMiddlewarePromise: Promise<NextProxy> | undefined;

async function getAuthMiddleware(): Promise<NextProxy> {
  authMiddlewarePromise ??= Promise.resolve(auth(authCallback));
  return authMiddlewarePromise;
}

export async function proxy(request: NextRequest, event: NextFetchEvent): Promise<NextResponse> {
  const config = getConfig();
  if (!requestHostMatchesOrigin(request, config.publicOrigin)) {
    const policy = csp(randomBytes(18).toString("base64"), process.env.NODE_ENV !== "production");
    const response = NextResponse.json(
      { error: { code: "MISDIRECTED_REQUEST", request_id: randomUUID() } },
      { status: 421, headers: { "Cache-Control": "no-store" } },
    );
    return secureHeaders(response, policy, config.devMode);
  }
  const nonce = randomBytes(18).toString("base64");
  const policy = csp(nonce, process.env.NODE_ENV !== "production");
  const requestHeaders = new Headers(request.headers);
  requestHeaders.set("Content-Security-Policy", policy);
  requestHeaders.set("x-nonce", nonce);

  const pathname = request.nextUrl.pathname;
  const protectedPage = pathname === "/" || pathname === "/select-tenant" || pathname.startsWith("/t/");
  const bffRoute = pathname.startsWith("/api/bff/");
  const authRoute = pathname.startsWith("/api/auth/") || pathname.startsWith("/auth/") || pathname === "/signed-out";
  let setCsrf: string | null = null;
  let lastTenant: string | null = null;

  if (protectedPage || bffRoute) {
    const sessionResult = await getRequestSession(request, { refresh: protectedPage });
    if (!sessionResult.session && protectedPage) {
      const response = loginRedirect(request, config.publicOrigin);
      if (sessionResult.clearSessionCookie) clearAuthCookies(response, request, config);
      return secureHeaders(response, policy, config.devMode);
    }
    if (!sessionResult.session && bffRoute && sessionResult.clearSessionCookie) {
      const response = unauthorized();
      clearAuthCookies(response, request, config);
      return secureHeaders(response, policy, config.devMode);
    }
    if (sessionResult.session) {
      if (sessionResult.updatedCookie) {
        requestHeaders.set("cookie", replaceSessionCookieHeader(request, sessionResult.updatedCookie, config.cookieNames.session));
      }
      const key = config.authSecrets[0];
      if (!sessionResult.session.csrfToken && key) {
        setCsrf = createCsrfToken(sessionResult.session.subject, key);
        requestHeaders.set("cookie", replaceCookie(requestHeaders.get("cookie") ?? "", config.cookieNames.csrf, setCsrf));
      }
      if (pathname.startsWith("/t/")) {
        const candidate = pathname.split("/")[2] ?? "";
        if (isTenantSlug(candidate)) {
          lastTenant = candidate;
          requestHeaders.set("cookie", replaceCookie(requestHeaders.get("cookie") ?? "", config.cookieNames.lastTenant, candidate));
        }
      }
    }
  }

  let response: NextResponse;
  if (authRoute || bffRoute) {
    response = NextResponse.next({ request: { headers: requestHeaders } });
  } else if (protectedPage) {
    try {
      const forwardedRequest = new NextRequest(request, { headers: requestHeaders });
      const authMiddleware = await getAuthMiddleware();
      const authResponse = await authMiddleware(forwardedRequest, event);
      if (!authResponse) {
        response = NextResponse.next({ request: { headers: requestHeaders } });
      } else if (authResponse.status === 200 && !authResponse.headers.has("location")) {
        response = NextResponse.next({ request: { headers: requestHeaders } });
        for (const cookie of authResponse.headers.getSetCookie()) response.headers.append("Set-Cookie", cookie);
      } else {
        response = toNextResponse(authResponse);
      }
    } catch {
      response = loginRedirect(request, config.publicOrigin);
      clearAuthCookies(response, request, config);
      return secureHeaders(response, policy, config.devMode);
    }
  } else {
    response = NextResponse.next({ request: { headers: requestHeaders } });
  }

  if (setCsrf) setCsrfCookie(response, setCsrf, config);
  if (lastTenant) setLastTenantCookie(response, lastTenant, config);
  return secureHeaders(response, policy, config.devMode);
}

export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico|robots.txt|sitemap.xml|.*\\.(?:svg|png|jpg|jpeg|gif|webp|ico|woff2?)$).*)"],
};
