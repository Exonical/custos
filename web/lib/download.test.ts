import { describe, expect, it } from "vitest";
import { filenameFromContentDisposition } from "@/lib/download";

describe("filenameFromContentDisposition", () => {
  it("uses attachment filenames and a fallback when absent", () => {
    expect(filenameFromContentDisposition('attachment; filename="proposal-v3.yaml"', "fallback.yaml")).toBe("proposal-v3.yaml");
    expect(filenameFromContentDisposition(null, "fallback.yaml")).toBe("fallback.yaml");
  });

  it("sanitizes path components and supports UTF-8 extended filenames", () => {
    expect(filenameFromContentDisposition('attachment; filename="../../danger.sbatch"', "fallback.sbatch")).toBe("danger.sbatch");
    expect(filenameFromContentDisposition("attachment; filename*=UTF-8''caf%C3%A9.yaml", "fallback.yaml")).toBe("café.yaml");
  });
});
