import { describe, expect, it } from "vitest";
import { requestHostMatchesOrigin } from "@/lib/security/host";

const origin = "https://127.0.0.1:3000";

function request(host: string, forwardedHost?: string): Request {
  const headers = new Headers({ Host: host });
  if (forwardedHost !== undefined) headers.set("X-Forwarded-Host", forwardedHost);
  return new Request(origin, { headers });
}

describe("configured request host allowlist", () => {
  it("accepts the e2e nginx Host and X-Forwarded-Host including port 3000", () => {
    expect(requestHostMatchesOrigin(request("127.0.0.1:3000", "127.0.0.1:3000"), origin)).toBe(true);
    expect(requestHostMatchesOrigin(request("127.0.0.1:3000"), origin)).toBe(true);
  });

  it("rejects a mismatching hostname or port", () => {
    expect(requestHostMatchesOrigin(request("attacker.invalid:3000"), origin)).toBe(false);
    expect(requestHostMatchesOrigin(request("127.0.0.1:3001"), origin)).toBe(false);
  });

  it("normalizes a default HTTP port", () => {
    expect(requestHostMatchesOrigin(request("127.0.0.1:80", "127.0.0.1:80"), "http://127.0.0.1")).toBe(true);
  });

  it("rejects a spoofed X-Forwarded-Host even when Host matches", () => {
    expect(requestHostMatchesOrigin(request("127.0.0.1:3000", "attacker.invalid:3000"), origin)).toBe(false);
  });
});
