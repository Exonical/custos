import { describe, expect, it } from "vitest";
import type { WebConfig } from "@/lib/config";
import { checkCsrf, createCsrfToken } from "@/lib/security/csrf";

const origin = "https://custos.test";
const authSecret = "csrf-test-secret-with-at-least-32-bytes";
const config = {
  publicOrigin: origin,
  authSecrets: [authSecret],
  cookieNames: { session: "__Host-custos_session", csrf: "__Host-custos_csrf", lastTenant: "__Host-custos_last_tenant" },
} as unknown as WebConfig;
const subject = "alice-subject";
const csrfToken = createCsrfToken(subject, authSecret);

function request(headers: Record<string, string> = {}) {
  return new Request(`${origin}/api/bff/tenants/acme/jobs`, {
    method: "POST",
    headers: {
      Origin: origin,
      "Sec-Fetch-Site": "same-origin",
      Cookie: `${config.cookieNames.csrf}=${csrfToken}`,
      "X-CSRF-Token": csrfToken,
      ...headers,
    },
  });
}

describe("BFF CSRF checks", () => {
  it("accepts a signed double-submit token bound to the current subject", () => {
    expect(checkCsrf(request(), { subject }, config)).toBe(true);
  });

  it("rejects a cookie signed for another subject", () => {
    const otherSubjectToken = createCsrfToken("bob-subject", authSecret);
    expect(checkCsrf(request({ Cookie: `${config.cookieNames.csrf}=${otherSubjectToken}`, "X-CSRF-Token": otherSubjectToken }), { subject }, config)).toBe(false);
  });

  it("rejects a tampered HMAC and a header/cookie mismatch", () => {
    const [random, mac] = csrfToken.split(".");
    const altered = `${random}.${mac[0] === "a" ? "b" : "a"}${mac.slice(1)}`;
    expect(checkCsrf(request({ Cookie: `${config.cookieNames.csrf}=${altered}`, "X-CSRF-Token": altered }), { subject }, config)).toBe(false);
    expect(checkCsrf(request({ "X-CSRF-Token": "different-token" }), { subject }, config)).toBe(false);
  });

  it("rejects an incorrect Origin or cross-site fetch", () => {
    expect(checkCsrf(request({ Origin: "https://evil.test" }), { subject }, config)).toBe(false);
    expect(checkCsrf(request({ "Sec-Fetch-Site": "cross-site" }), { subject }, config)).toBe(false);
  });

  it("permits an absent Sec-Fetch-Site but requires Origin and both tokens", () => {
    const noFetchSite = new Request(`${origin}/api/bff/tenants/acme/jobs`, {
      method: "POST",
      headers: { Origin: origin, Cookie: `${config.cookieNames.csrf}=${csrfToken}`, "X-CSRF-Token": csrfToken },
    });
    expect(checkCsrf(noFetchSite, { subject }, config)).toBe(true);
    expect(checkCsrf(request({ Cookie: "" }), { subject }, config)).toBe(false);
  });
});
