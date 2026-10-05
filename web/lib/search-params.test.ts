import { describe, expect, it } from "vitest";
import { first } from "@/lib/search-params";

describe("first", () => {
  it("returns a single value unchanged", () => {
    expect(first("running")).toBe("running");
  });

  it("returns the first element of a repeated param", () => {
    expect(first(["a", "b"])).toBe("a");
  });

  it("returns an empty string for missing values and empty arrays", () => {
    expect(first(undefined)).toBe("");
    expect(first([])).toBe("");
  });
});
