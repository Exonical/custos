import { describe, expect, it } from "vitest";
import { detailText, extractErrorEnvelope } from "./error-details";

describe("detailText", () => {
  it("renders field-scoped reasons and skips malformed entries", () => {
    expect(detailText([
      { field: "spec.tasks[0].resources.walltime", reason: "invalid walltime" },
      { reason: "no field" },
      { field: "x" },
      null,
      "text",
      { field: 1, reason: "numeric field" },
    ])).toBe("spec.tasks[0].resources.walltime: invalid walltime · no field · numeric field");
  });

  it("returns an empty string when nothing is usable", () => {
    expect(detailText([{ message: "legacy shape" }])).toBe("");
  });
});

describe("extractErrorEnvelope", () => {
  it("returns typed fields from an apperr body", () => {
    const details = [{ field: "name", reason: "required" }];
    expect(extractErrorEnvelope({ error: { code: "VALIDATION", message: "invalid", details } }))
      .toEqual({ code: "VALIDATION", message: "invalid", details });
  });

  it("drops mistyped fields", () => {
    expect(extractErrorEnvelope({ error: { code: 1, message: null, details: { field: "x" } } })).toEqual({});
  });

  it("tolerates non-envelope payloads", () => {
    for (const value of [null, undefined, "text", 42, [], {}, { error: "boom" }, { error: null }]) {
      expect(extractErrorEnvelope(value)).toEqual({});
    }
  });
});
