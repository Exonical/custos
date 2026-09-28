import { describe, expect, it } from "vitest";
import { safeEqual } from "@/lib/security/crypto";

describe("constant-time string equality", () => {
  it("matches equal strings and rejects different values or lengths", () => {
    expect(safeEqual("csrf-token", "csrf-token")).toBe(true);
    expect(safeEqual("csrf-token", "other-token")).toBe(false);
    expect(safeEqual("short", "longer")).toBe(false);
  });
});
