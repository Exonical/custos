import { describe, expect, it } from "vitest";
import type { Account, Session } from "next-auth";
import type { JWT } from "next-auth/jwt";
import { isUsableAuthState, jwtCallback, sessionCallback, type TokenRefresh } from "@/lib/auth/callbacks";
import { resetRefreshFlightsForTest } from "@/lib/security/refresh-singleflight";

const subject = "alice-subject";
const now = 1_800_000_000_000;

function jwt(expiresAt = now + 3_600_000): JWT {
  return {
    sub: subject,
    name: "Alice",
    email: "alice@example.test",
    access_token: "access-old",
    refresh_token: "refresh-shared",
    expires_at: expiresAt,
  };
}

function tokenRefresh(accessToken = "access-new", refreshToken = "refresh-new"): TokenRefresh {
  return () => Promise.resolve({
    access_token: accessToken,
    refresh_token: refreshToken,
    expiresIn: () => 3600,
  });
}

describe("Auth.js JWT and session callbacks", () => {
  it("does not expose any token from the encrypted JWT in the browser session", () => {
    const input: Session = {
      expires: new Date(now + 60_000).toISOString(),
      user: { sub: subject, name: "Alice", email: "alice@example.test" },
    };
    const output = sessionCallback(input, {
      ...jwt(),
      access_token: "ACCESS_SECRET",
      refresh_token: "REFRESH_SECRET",
      id_token: "ID_SECRET",
    });
    expect(output).toEqual({
      expires: input.expires,
      user: { sub: subject, name: "Alice", email: "alice@example.test" },
    });
    expect(JSON.stringify(output)).not.toMatch(/ACCESS_SECRET|REFRESH_SECRET|ID_SECRET/);
  });

  it("does not call the token endpoint before the 60-second refresh window", async () => {
    let calls = 0;
    const result = await jwtCallback(
      { token: jwt(now + 61_000), account: null },
      { now, refresh: () => { calls += 1; return Promise.resolve({ access_token: "unused", expiresIn: () => 60 }); } },
    );
    expect(calls).toBe(0);
    expect(result.access_token).toBe("access-old");
  });

  it("refreshes an access token with less than 60 seconds remaining", async () => {
    const result = await jwtCallback(
      { token: jwt(now + 59_999), account: null },
      { now, refresh: tokenRefresh() },
    );
    expect(result.access_token).toBe("access-new");
    expect(result.refresh_token).toBe("refresh-new");
    expect(result.error).toBeUndefined();
  });

  it("coalesces concurrent callback refreshes into one token endpoint call", async () => {
    resetRefreshFlightsForTest();
    let calls = 0;
    let finish!: (value: { access_token: string; refresh_token: string; expiresIn(): number }) => void;
    const refresh: TokenRefresh = () => {
      calls += 1;
      return new Promise((resolve) => { finish = resolve; });
    };
    const first = jwtCallback({ token: jwt(now + 1), account: null }, { now, refresh });
    const second = jwtCallback({ token: jwt(now + 1), account: null }, { now, refresh });
    await Promise.resolve();
    expect(calls).toBe(1);
    finish({ access_token: "access-one", refresh_token: "refresh-one", expiresIn: () => 3600 });
    const [a, b] = await Promise.all([first, second]);
    expect(a.access_token).toBe("access-one");
    expect(b.access_token).toBe("access-one");
  });

  it("marks refresh failure and drops both tokens", async () => {
    const result = await jwtCallback(
      { token: jwt(now), account: null },
      { now, refresh: () => Promise.reject(new Error("IdP unavailable")) },
    );
    expect(result.error).toBe("RefreshTokenError");
    expect(result.access_token).toBeUndefined();
    expect(result.refresh_token).toBeUndefined();
  });

  it("stores access and refresh tokens from the OIDC account but never the ID token", async () => {
    const account = {
      provider: "custos",
      type: "oidc",
      providerAccountId: subject,
      access_token: "account-access",
      refresh_token: "account-refresh",
      expires_at: Math.floor((now + 300_000) / 1000),
      id_token: "account-id-token",
    } satisfies Account;
    const initialToken: JWT = { sub: subject };
    const result = await jwtCallback({ token: initialToken, account });
    expect(result.access_token).toBe("account-access");
    expect(result.refresh_token).toBe("account-refresh");
    expect(result.id_token).toBeUndefined();
  });

  it("fails closed for missing/config-error sessions, errors, subject mismatch, or missing access", () => {
    const good = { expires: new Date(now).toISOString(), user: { sub: subject, name: null, email: null } } as Session;
    expect(isUsableAuthState(good, jwt())).toBe(true);
    expect(isUsableAuthState(null, jwt())).toBe(false);
    expect(isUsableAuthState({ ...good, error: "RefreshTokenError" }, jwt())).toBe(false);
    expect(isUsableAuthState(good, { ...jwt(), access_token: undefined })).toBe(false);
    expect(isUsableAuthState(good, { ...jwt(), error: "RefreshTokenError" })).toBe(false);
    expect(isUsableAuthState({ ...good, user: { ...good.user, sub: "bob" } }, jwt())).toBe(false);
  });
});
