import { describe, expect, it } from "vitest";
import { clientAddress } from "@/lib/security/client-address";

function request(headers: HeadersInit): Request {
  return new Request("http://custos.test", { headers });
}

describe("trusted proxy client address", () => {
  it("ignores forwarded and real-IP headers when no proxy hop is trusted", () => {
    const incoming = request({
      "X-Forwarded-For": "198.51.100.3",
      "X-Real-IP": "203.0.113.8",
    });
    expect(clientAddress(incoming, 0)).toBeUndefined();
  });

  it("selects the right-most address in the forwarded chain", () => {
    const incoming = request({ "X-Forwarded-For": "198.51.100.3, 10.2.0.9" });
    expect(clientAddress(incoming, 1)).toBe("10.2.0.9");
  });

  it("omits a missing, short, or invalid forwarded value", () => {
    expect(clientAddress(request({}), 1)).toBeUndefined();
    expect(clientAddress(request({ "X-Forwarded-For": "198.51.100.3" }), 2)).toBeUndefined();
    expect(clientAddress(request({ "X-Forwarded-For": "198.51.100.3, not-an-ip" }), 1)).toBeUndefined();
  });
});
