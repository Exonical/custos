import { describe, expect, it } from "vitest";
import {
  parseModuleNames,
  validateClusterSettings,
  type ClusterSettingsDraft,
} from "@/lib/clusters/settings";

function draft(overrides: Partial<ClusterSettingsDraft> = {}): ClusterSettingsDraft {
  return {
    containerEnabled: true,
    runtimeType: "apptainer",
    binary: "apptainer",
    allowedImagePrefixes: "",
    requireDigest: false,
    slurmInContainer: false,
    mpiPlugin: "pmix",
    softwareModules: [],
    ...overrides,
  };
}

describe("validateClusterSettings", () => {
  it("rejects invalid runtime and module fields and duplicate name/version pairs", () => {
    const result = validateClusterSettings(draft({
      binary: "apptainer; rm",
      allowedImagePrefixes: "docker://trusted.example/..\nnot-an-image",
      mpiPlugin: "PMIX",
      softwareModules: [
        { id: "1", name: "openmpi", version: "5", modules: "gcc/13 openmpi/5" },
        { id: "2", name: "openmpi", version: "5", modules: "gcc/13" },
        { id: "3", name: "bad name", version: "", modules: "bad$module" },
      ],
    }));
    expect(result.valid).toBe(false);
    if (!result.valid) {
      expect(result.errors.binary).toBeDefined();
      expect(result.errors.allowedImagePrefixes).toBeDefined();
      expect(result.errors.mpiPlugin).toBeDefined();
      expect(result.errors["softwareModules.2.name"]).toContain("already listed");
      expect(result.errors["softwareModules.3.name"]).toBeDefined();
      expect(result.errors["softwareModules.3.modules"]).toBeDefined();
    }
  });

  it("serializes disabled runtime as null and omits the runtime object", () => {
    const result = validateClusterSettings(draft({
      containerEnabled: false,
      softwareModules: [{ id: "1", name: "openmpi", version: "", modules: "gcc/13, openmpi/5" }],
    }));
    expect(result.valid).toBe(true);
    if (result.valid) {
      expect(result.payload.container_runtime).toBeNull();
      expect(result.payload.software_modules).toEqual([
        { name: "openmpi", modules: ["gcc/13", "openmpi/5"] },
      ]);
    }
  });

  it("omits Apptainer-only binary for Pyxis and accepts an empty image prefix list", () => {
    const result = validateClusterSettings(draft({
      runtimeType: "pyxis",
      binary: "this-is-ignored",
      allowedImagePrefixes: " \n",
      mpiPlugin: "",
    }));
    expect(result.valid).toBe(true);
    if (result.valid) {
      expect(result.payload.container_runtime).toEqual({
        type: "pyxis",
        allowed_image_prefixes: [],
        require_digest: false,
        slurm_in_container: false,
      });
    }
  });

  it("parses space- and comma-separated module names", () => {
    expect(parseModuleNames("gcc/13, openmpi/5\tcuda/12")).toEqual(["gcc/13", "openmpi/5", "cuda/12"]);
  });
});
