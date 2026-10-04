import { describe, expect, it } from "vitest";
import { SBATCH_IMPORT_MAX_BODY_BYTES, SBATCH_IMPORT_MAX_SCRIPT_BYTES, validateSbatchImport } from "@/lib/workflow/sbatch-import";

describe("validateSbatchImport", () => {
  it("requires one through twenty scripts", () => {
    const empty = validateSbatchImport("", []);
    const tooMany = validateSbatchImport("", Array.from({ length: 21 }, (_, index) => ({
      filename: `${String(index)}.sh`,
      content: "echo ok",
    })));
    expect(empty.valid ? "" : empty.error).toContain("at least one");
    expect(tooMany.valid ? "" : tooMany.error).toContain("no more than 20");
  });

  it("checks UTF-8 bytes per script", () => {
    const validation = validateSbatchImport("", [{
      filename: "large.sh",
      content: "😀".repeat(Math.ceil((SBATCH_IMPORT_MAX_SCRIPT_BYTES + 1) / 4)),
    }]);
    expect(validation.valid).toBe(false);
    if (!validation.valid) expect(validation.error).toContain("256 KiB");
  });

  it("checks the encoded JSON request size and trims optional names", () => {
    const oversized = validateSbatchImport("", Array.from({ length: 10 }, (_, index) => ({
      filename: `${String(index)}.sh`,
      content: "x".repeat(230_000),
    })));
    expect(oversized.valid).toBe(false);
    if (!oversized.valid) expect(oversized.error).toContain("Import fewer files");

    const valid = validateSbatchImport("  proposal  ", [{ filename: "a.sh", content: "echo hi" }]);
    expect(valid.valid).toBe(true);
    if (valid.valid) {
      expect(valid.request.name).toBe("proposal");
      expect(valid.bodyBytes).toBeLessThanOrEqual(SBATCH_IMPORT_MAX_BODY_BYTES);
    }
  });
});
