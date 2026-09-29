import { describe, expect, it } from "vitest";
import { detailText } from "./error-details";

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
