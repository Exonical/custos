import { describe, expect, it } from "vitest";
import { isTenantSlug } from "@/lib/security/tenant-slug";

describe("last tenant cookie slug validation", () => {
  it("accepts bounded lowercase tenant slugs", () => {
    expect(isTenantSlug("acme")).toBe(true);
    expect(isTenantSlug("hpc-a")).toBe(true);
  });

  it("rejects empty, uppercase, path, and overlong values", () => {
    expect(isTenantSlug("")).toBe(false);
    expect(isTenantSlug("ACME")).toBe(false);
    expect(isTenantSlug("../admin")).toBe(false);
    expect(isTenantSlug("a".repeat(64))).toBe(false);
  });
});
