import { describe, expect, it } from "vitest";
import { safeReturnTo } from "@/lib/security/return-to";

describe("returnTo validation", () => {
  it("accepts same-origin relative paths and preserves query strings", () => {
    expect(safeReturnTo("/t/acme/jobs?state=RUNNING")).toBe("/t/acme/jobs?state=RUNNING");
  });

  it("falls back to root for absolute, protocol-relative, and malformed targets", () => {
    for (const value of ["https://evil.test/", "//evil.test", "", null, undefined, "\\\\evil.test", "/\\evil.test", "/bad\u0000path"]) {
      expect(safeReturnTo(value)).toBe("/");
    }
  });
});
