import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSafeFetch } from "./http";

const { agentMock, undiciFetchMock } = vi.hoisted(() => ({
  agentMock: vi.fn(),
  undiciFetchMock: vi.fn(),
}));

vi.mock("undici", () => ({
  Agent: agentMock,
  fetch: undiciFetchMock,
}));

let tempDirectory: string | undefined;

afterEach(() => {
  if (tempDirectory) rmSync(tempDirectory, { recursive: true, force: true });
  tempDirectory = undefined;
  vi.clearAllMocks();
});

describe("createSafeFetch", () => {
  it("normalizes cross-runtime Request inputs before dispatching through an undici agent", async () => {
    tempDirectory = mkdtempSync(join(tmpdir(), "custos-safe-fetch-"));
    const caFile = join(tempDirectory, "ca.pem");
    writeFileSync(caFile, "test CA");
    undiciFetchMock.mockResolvedValue(new Response("ok"));
    const request = new Request("https://keycloak.e2e/token", {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: "grant_type=authorization_code",
    });

    const response = await createSafeFetch(caFile, 1_000)(request);

    expect(await response.text()).toBe("ok");
    expect(undiciFetchMock).toHaveBeenCalledWith(
      request.url,
      expect.objectContaining({
        method: "POST",
        headers: request.headers,
        body: request.body,
        duplex: "half",
      }),
    );
    expect(agentMock).toHaveBeenCalledOnce();
  });
});
