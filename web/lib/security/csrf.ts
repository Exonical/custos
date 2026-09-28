import { createHmac, hkdfSync, randomBytes } from "node:crypto";
import type { NextResponse } from "next/server";
import type { WebConfig } from "@/lib/config";
import { getCookieValue } from "@/lib/security/cookies";
import { safeEqual } from "@/lib/security/crypto";

const CSRF_HKDF_INFO = Buffer.from("custos-csrf", "utf8");

function csrfKey(authSecret: string): Buffer {
  return Buffer.from(hkdfSync("sha256", Buffer.from(authSecret, "utf8"), Buffer.alloc(0), CSRF_HKDF_INFO, 32));
}

export function createCsrfToken(subject: string, authSecret: string): string {
  const random = randomBytes(32).toString("base64url");
  const mac = createHmac("sha256", csrfKey(authSecret)).update(`${subject}.${random}`, "utf8").digest("base64url");
  return `${random}.${mac}`;
}

export function verifyCsrfToken(token: string, subject: string, authSecret: string): boolean {
  const [random, mac, ...extra] = token.split(".");
  if (extra.length > 0 || !random || !mac || !/^[A-Za-z0-9_-]{43}$/.test(random)) return false;
  const expected = createHmac("sha256", csrfKey(authSecret)).update(`${subject}.${random}`, "utf8").digest("base64url");
  return safeEqual(mac, expected);
}

export function checkCsrf(
  request: Request,
  session: Readonly<{ subject: string }>,
  config: WebConfig,
): boolean {
  if (request.headers.get("origin") !== config.publicOrigin) return false;
  const fetchSite = request.headers.get("sec-fetch-site");
  if (fetchSite !== null && fetchSite !== "same-origin") return false;
  const headerToken = request.headers.get("x-csrf-token");
  const cookieToken = getCookieValue(request, config.cookieNames.csrf);
  const key = config.authSecrets[0];
  if (!key || !headerToken || !cookieToken || !safeEqual(headerToken, cookieToken)) return false;
  return verifyCsrfToken(cookieToken, session.subject, key);
}

export function setCsrfCookie(response: NextResponse, value: string, config: WebConfig): void {
  response.cookies.set(config.cookieNames.csrf, value, {
    path: "/",
    secure: config.cookieSecure,
    httpOnly: false,
    sameSite: "lax",
    maxAge: 8 * 60 * 60,
  });
}

export function clearCsrfCookie(response: NextResponse, config: WebConfig): void {
  response.cookies.set(config.cookieNames.csrf, "", {
    path: "/",
    secure: config.cookieSecure,
    httpOnly: false,
    sameSite: "lax",
    maxAge: 0,
  });
}
