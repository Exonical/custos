import { encode, getToken } from "next-auth/jwt";
import { describe, expect, it } from "vitest";
import { authSessionCookiePairs, replaceSessionCookieHeader } from "@/lib/session/request";

const authSecrets = ["test-auth-secret-with-at-least-32-characters"];
const cookieName = "__Host-custos_session";
async function encodeToken(accessToken: string, refreshToken: string, secrets = authSecrets): Promise<string> {
  return encode({
    token: {
      sub: "alice-subject",
      access_token: accessToken,
      refresh_token: refreshToken,
      expires_at: Date.now() + 60_000,
    },
    secret: secrets,
    salt: cookieName,
    maxAge: 8 * 60 * 60,
  });
}

describe("Auth.js session cookie forwarding", () => {
  it("forwards refreshed Auth.js chunks so getToken sees them in the same request", async () => {
    const oldValue = await encodeToken("old-access", "old-refresh");
    const refreshedValue = await encodeToken("new-access-".repeat(500), "new-refresh-".repeat(500));
    const oldCookies = authSessionCookiePairs(oldValue, cookieName);
    const request = new Request("https://custos.test/t/acme", {
      headers: { cookie: oldCookies.map(([name, value]) => `${name}=${value}`).join("; ") },
    });
    const forwarded = replaceSessionCookieHeader(request, refreshedValue, cookieName);
    const forwardedRequest = new Request(request.url, { headers: { cookie: forwarded } });
    const names = forwarded.split(";").map((part) => part.trim().split("=", 1)[0]);
    const decoded = await getToken({
      req: forwardedRequest,
      secret: authSecrets,
      salt: cookieName,
      secureCookie: true,
      cookieName,
    });
    expect(authSessionCookiePairs(refreshedValue, cookieName).length).toBeGreaterThan(1);
    expect(names).toContain(`${cookieName}.0`);
    expect(names).not.toContain(cookieName);
    expect(decoded?.access_token).toBe("new-access-".repeat(500));
    expect(decoded?.refresh_token).toBe("new-refresh-".repeat(500));
  });

  it("replaces stale chunks when a refreshed session becomes smaller", async () => {
    const large = await encodeToken("access-".repeat(1000), "refresh-".repeat(1000));
    const small = await encodeToken("small-access", "small-refresh");
    const oldCookies = authSessionCookiePairs(large, cookieName);
    const request = new Request("https://custos.test/t/acme", {
      headers: { cookie: oldCookies.map(([name, value]) => `${name}=${value}`).join("; ") },
    });
    const forwarded = replaceSessionCookieHeader(request, small, cookieName);
    const names = forwarded.split(";").map((part) => part.trim().split("=", 1)[0]);
    expect(names).toContain(cookieName);
    expect(names.some((name) => name.startsWith(`${cookieName}.`))).toBe(false);
  });

  it("decrypts with the previous Auth.js secret and reseals with the first secret", async () => {
    const oldSecrets = ["old-auth-secret-with-at-least-32-bytes"];
    const newSecrets = ["new-auth-secret-with-at-least-32-bytes", ...oldSecrets];
    const oldValue = await encodeToken("old-access", "old-refresh", oldSecrets);
    const request = new Request("https://custos.test/", { headers: { cookie: `${cookieName}=${oldValue}` } });
    const decodedWithRotation = await getToken({
      req: request,
      secret: newSecrets,
      salt: cookieName,
      secureCookie: true,
      cookieName,
    });
    expect(decodedWithRotation?.access_token).toBe("old-access");
    const resealed = await encode({ token: decodedWithRotation ?? {}, secret: newSecrets, salt: cookieName, maxAge: 8 * 60 * 60 });
    const nextRequest = new Request("https://custos.test/", { headers: { cookie: `${cookieName}=${resealed}` } });
    const decodedWithNewKey = await getToken({
      req: nextRequest,
      secret: [newSecrets[0]],
      salt: cookieName,
      secureCookie: true,
      cookieName,
    });
    expect(decodedWithNewKey?.access_token).toBe("old-access");
  });
});
