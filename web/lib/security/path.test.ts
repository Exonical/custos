import { describe, expect, it } from "vitest";
import { allowedRequestHeaders, copyResponseHeaders, sanitizeBffPath } from "@/lib/security/path";

describe("BFF proxy path and headers", () => {
  it("accepts ordinary API paths and preserves encoded non-separator characters", () => {
    expect(sanitizeBffPath("/api/bff/tenants/acme/jobs")).toBe("tenants/acme/jobs");
    expect(sanitizeBffPath("/api/bff/tenants/acme%20lab/jobs")).toBe("tenants/acme%20lab/jobs");
  });

  it("rejects empty, dot, traversal, encoded slash/backslash/null, and double-encoded separators", () => {
    for (const path of [
      "/api/bff/",
      "/api/bff//jobs",
      "/api/bff/./jobs",
      "/api/bff/../me",
      "/api/bff/%2fetc",
      "/api/bff/%5cetc",
      "/api/bff/%00x",
      "/api/bff/%252fetc",
      "/api/bff/%255cetc",
    ]) {
      expect(sanitizeBffPath(path)).toBeNull();
    }
  });

  it("only forwards the documented request headers", () => {
    const input = new Headers({
      Accept: "application/json",
      "Content-Type": "application/json",
      "Idempotency-Key": "key",
      "If-Match": "version",
      "X-Expected-Version": "12",
      Cookie: "secret",
      Authorization: "Bearer should-not-pass",
      "X-CSRF-Token": "csrf",
    });
    const output = allowedRequestHeaders(input);
    expect([...output.keys()].sort()).toEqual(["accept", "content-type", "idempotency-key", "if-match", "x-expected-version"]);
  });

  it("forwards expected versions only as bounded decimal integers", () => {
    const valid = allowedRequestHeaders(new Headers({ "X-Expected-Version": "9223372036854775807" }));
    const invalid = allowedRequestHeaders(new Headers({ "X-Expected-Version": "12;drop" }));
    expect(valid.get("x-expected-version")).toBe("9223372036854775807");
    expect(invalid.has("x-expected-version")).toBe(false);
  });

  it("preserves non-JSON Accept values for downloads", () => {
    const output = allowedRequestHeaders(new Headers({
      Accept: "application/yaml",
      "Content-Type": "application/yaml",
      "X-Expected-Version": "4",
    }));
    expect(output.get("accept")).toBe("application/yaml");
    expect(output.get("content-type")).toBe("application/yaml");
    expect(output.get("x-expected-version")).toBe("4");
  });

  it("strips cookies and hop-by-hop response headers and forces no-store", () => {
    const output = copyResponseHeaders(new Headers({
      "Content-Type": "application/json",
      "Content-Disposition": 'attachment; filename="workflow.yaml"',
      "Set-Cookie": "secret=x",
      Connection: "x-internal, keep-alive",
      "X-Internal": "drop",
      "Keep-Alive": "timeout=5",
    }));
    expect(output.get("content-type")).toBe("application/json");
    expect(output.get("content-disposition")).toBe('attachment; filename="workflow.yaml"');
    expect(output.has("set-cookie")).toBe(false);
    expect(output.has("x-internal")).toBe(false);
    expect(output.has("keep-alive")).toBe(false);
    expect(output.get("cache-control")).toBe("no-store");
  });
});
