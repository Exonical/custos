import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  auth: vi.fn(),
  getConfig: vi.fn(),
  cookies: vi.fn(),
  headers: vi.fn(),
  getRequestSession: vi.fn(),
  requestAuthToken: vi.fn(),
}));

vi.mock("@/auth", () => ({ auth: mocks.auth }));
vi.mock("@/lib/config", () => ({ getConfig: mocks.getConfig }));
vi.mock("next/headers", () => ({ cookies: mocks.cookies, headers: mocks.headers }));
vi.mock("@/lib/session/request", () => ({
  getRequestSession: mocks.getRequestSession,
  requestAuthToken: mocks.requestAuthToken,
}));

import { getServerSession } from "@/lib/session/server";

const subject = "alice-subject";
const authSession = {
  user: { sub: subject, name: "Alice", email: "alice@example.test" },
  expires: new Date(Date.now() + 60_000).toISOString(),
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.auth.mockResolvedValue(authSession);
  mocks.getConfig.mockReturnValue({ publicOrigin: "https://custos.test" });
  mocks.cookies.mockResolvedValue({ getAll: () => [{ name: "__Host-custos_session", value: "opaque" }] });
  mocks.headers.mockResolvedValue(new Headers());
  mocks.requestAuthToken.mockResolvedValue({ sub: subject, access_token: "server-access" });
  mocks.getRequestSession.mockResolvedValue({
    session: { subject, accessToken: "server-access", refreshToken: "server-refresh", csrfToken: "csrf" },
    updatedCookie: null,
    clearSessionCookie: false,
  });
});

describe("getServerSession fail-closed behavior", () => {
  it("returns only server session fields when Auth.js and the encrypted JWT agree", async () => {
    await expect(getServerSession()).resolves.toEqual({
      subject,
      accessToken: "server-access",
      csrfToken: "csrf",
    });
  });

  it("fails closed when Auth.js configuration throws", async () => {
    mocks.auth.mockRejectedValue(new Error("configuration error"));
    await expect(getServerSession()).resolves.toBeNull();
  });

  it("fails closed for Auth.js errors, missing access tokens, and configuration errors", async () => {
    mocks.auth.mockResolvedValue({ ...authSession, error: "RefreshTokenError" });
    await expect(getServerSession()).resolves.toBeNull();

    mocks.auth.mockResolvedValue(authSession);
    mocks.requestAuthToken.mockResolvedValue({ sub: subject });
    await expect(getServerSession()).resolves.toBeNull();

    mocks.requestAuthToken.mockResolvedValue({ sub: subject, access_token: "server-access" });
    mocks.getConfig.mockImplementation(() => { throw new Error("missing configuration"); });
    await expect(getServerSession()).resolves.toBeNull();
  });
});
