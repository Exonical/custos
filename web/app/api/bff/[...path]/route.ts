import { randomUUID } from "node:crypto";
import { NextRequest, NextResponse } from "next/server";
import { getConfig } from "@/lib/config";
import { createSafeFetch } from "@/lib/http";
import { clientAddress } from "@/lib/security/client-address";
import { allowedRequestHeaders, copyResponseHeaders, sanitizeBffPath } from "@/lib/security/path";
import { checkCsrf } from "@/lib/security/csrf";
import { clearAuthCookies, writeAuthSessionCookie } from "@/lib/session/auth-cookie";
import { getRequestSession } from "@/lib/session/request";
import type { RequestSession, RequestSessionResult } from "@/lib/session/model";

const MAX_BODY_BYTES = 2 * 1024 * 1024;
const TIMEOUT_MS = 30_000;
const METHODS = new Set(["GET", "POST", "PATCH", "PUT", "DELETE"]);

function requestId(request: Request): string {
  const value = request.headers.get("x-request-id");
  return value && /^[A-Za-z0-9._:-]{1,128}$/.test(value) ? value : randomUUID();
}

function errorResponse(status: number, code: string, id: string): NextResponse {
  return NextResponse.json(
    { error: { code, message: code === "UNAUTHENTICATED" ? "Authentication required" : "Request rejected", request_id: id } },
    { status, headers: { "Cache-Control": "no-store", "X-Request-ID": id } },
  );
}

async function readBodyLimited(request: Request): Promise<Uint8Array<ArrayBuffer> | null> {
  if (!request.body) return null;
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > MAX_BODY_BYTES) {
        await reader.cancel();
        throw new BodyTooLargeError();
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const body = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return body;
}

class BodyTooLargeError extends Error {}

function upstreamUrl(request: NextRequest, path: string): URL {
  const config = getConfig();
  const target = new URL(`/api/v1/${path}`, config.apiUrl);
  target.search = request.nextUrl.search;
  return target;
}

async function callUpstream(
  request: NextRequest,
  path: string,
  session: RequestSession,
  body: Uint8Array<ArrayBuffer> | null,
  id: string,
): Promise<Response> {
  const config = getConfig();
  const headers = allowedRequestHeaders(request.headers);
  headers.set("Authorization", `Bearer ${session.accessToken}`);
  headers.set("X-Request-ID", id);
  const forwardedClientAddress = clientAddress(request, config.trustedProxyHops);
  if (forwardedClientAddress) headers.set("X-Forwarded-For", forwardedClientAddress);
  return createSafeFetch(config.apiCaFile, TIMEOUT_MS)(upstreamUrl(request, path), {
    method: request.method,
    headers,
    body: body ?? undefined,
    redirect: "manual",
    signal: AbortSignal.timeout(TIMEOUT_MS),
  });
}

function withSessionCookies(
  response: NextResponse,
  result: RequestSessionResult,
  request: Request,
): NextResponse {
  const config = getConfig();
  if (result.clearSessionCookie) clearAuthCookies(response, request, config);
  else if (result.updatedCookie) writeAuthSessionCookie(response, request, result.updatedCookie, config);
  response.headers.set("Cache-Control", "no-store");
  return response;
}

async function handle(request: NextRequest): Promise<NextResponse> {
  const id = requestId(request);
  if (!METHODS.has(request.method)) return errorResponse(405, "METHOD_NOT_ALLOWED", id);

  const path = sanitizeBffPath(request.nextUrl.pathname);
  if (!path) return errorResponse(400, "PATH_INVALID", id);

  const sessionResult = await getRequestSession(request, { reseal: true });
  if (!sessionResult.session) {
    return withSessionCookies(errorResponse(401, "UNAUTHENTICATED", id), sessionResult, request);
  }
  const config = getConfig();

  if (request.method !== "GET" && request.method !== "HEAD" && !checkCsrf(request, sessionResult.session, config)) {
    return withSessionCookies(errorResponse(403, "CSRF_REJECTED", id), sessionResult, request);
  }

  const contentLength = request.headers.get("content-length");
  if (contentLength && Number(contentLength) > MAX_BODY_BYTES) {
    return withSessionCookies(errorResponse(413, "BODY_TOO_LARGE", id), sessionResult, request);
  }

  let body: Uint8Array<ArrayBuffer> | null;
  try {
    body = request.method === "GET" || request.method === "HEAD" ? null : await readBodyLimited(request);
  } catch (error) {
    const status = error instanceof BodyTooLargeError ? 413 : 400;
    return withSessionCookies(errorResponse(status, status === 413 ? "BODY_TOO_LARGE" : "BODY_INVALID", id), sessionResult, request);
  }

  let current = sessionResult;
  let currentSession: RequestSession = sessionResult.session;
  let upstream: Response;
  try {
    upstream = await callUpstream(request, path, currentSession, body, id);
  } catch {
    return withSessionCookies(errorResponse(502, "UPSTREAM_UNAVAILABLE", id), current, request);
  }

  if (upstream.status === 401) {
    await upstream.body?.cancel().catch(() => undefined);
    const refreshed = await getRequestSession(request, {
      forceRefreshAccessToken: currentSession.accessToken,
      reseal: true,
    });
    if (!refreshed.session || refreshed.session.accessToken === currentSession.accessToken) {
      return withSessionCookies(errorResponse(401, "UNAUTHENTICATED", id), refreshed, request);
    }
    current = refreshed;
    currentSession = refreshed.session;
    try {
      upstream = await callUpstream(request, path, currentSession, body, id);
    } catch {
      return withSessionCookies(errorResponse(502, "UPSTREAM_UNAVAILABLE", id), current, request);
    }
  }

  const headers = copyResponseHeaders(upstream.headers);
  headers.set("X-Request-ID", id);
  const responseBody = upstream.status === 204 || upstream.status === 205 || upstream.status === 304
    ? null
    : upstream.body;
  const response = new NextResponse(responseBody, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers,
  });
  return withSessionCookies(response, current, request);
}

export const GET = handle;
export const POST = handle;
export const PATCH = handle;
export const PUT = handle;
export const DELETE = handle;
export const runtime = "nodejs";
