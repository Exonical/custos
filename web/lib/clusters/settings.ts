import type { components } from "@/lib/api/schema";

export type ContainerRuntime = components["schemas"]["ContainerRuntime"];
export type SoftwareModule = components["schemas"]["SoftwareModule"];

export type SoftwareModuleDraft = {
  id: string;
  name: string;
  version: string;
  modules: string;
};

export type ClusterSettingsDraft = {
  containerEnabled: boolean;
  runtimeType: "apptainer" | "pyxis";
  binary: string;
  allowedImagePrefixes: string;
  requireDigest: boolean;
  slurmInContainer: boolean;
  mpiPlugin: string;
  softwareModules: SoftwareModuleDraft[];
};

export type ContainerRuntimePatch = {
  type: "apptainer" | "pyxis";
  binary?: string;
  allowed_image_prefixes: string[];
  require_digest: boolean;
  slurm_in_container: boolean;
  mpi_plugin?: string;
};

export type ClusterSettingsPayload = {
  container_runtime: ContainerRuntimePatch | null;
  software_modules: SoftwareModule[];
};

export type ClusterSettingsValidation =
  | { valid: true; payload: ClusterSettingsPayload; errors: Record<string, never> }
  | { valid: false; errors: Record<string, string> };

const binaryPattern = /^[A-Za-z0-9_.:@/=-]+$/;
const imagePrefixPattern = /^(docker|oras):\/\/[A-Za-z0-9._/:@+-]+$|^\/[A-Za-z0-9._/+-]+$/;
const mpiPluginPattern = /^[a-z0-9_]+$/;
const softwareNamePattern = /^[A-Za-z0-9][A-Za-z0-9_.+-]{0,63}$/;
const moduleNamePattern = /^[A-Za-z0-9][A-Za-z0-9_.+/@-]{0,127}$/;

export function clusterSettingsDraft(
  runtime: ContainerRuntime | null,
  softwareModules: SoftwareModule[] | undefined,
): ClusterSettingsDraft {
  return {
    containerEnabled: runtime !== null,
    runtimeType: runtime?.type ?? "apptainer",
    binary: runtime?.binary ?? "apptainer",
    allowedImagePrefixes: (runtime?.allowed_image_prefixes ?? []).join("\n"),
    requireDigest: runtime?.require_digest ?? false,
    slurmInContainer: runtime?.slurm_in_container ?? false,
    mpiPlugin: runtime?.mpi_plugin ?? "pmix",
    softwareModules: (softwareModules ?? []).map((module, index) => ({
      id: `module-${String(index)}`,
      name: module.name,
      version: module.version ?? "",
      modules: module.modules.join(" "),
    })),
  };
}

export function parseModuleNames(value: string): string[] {
  return value.split(/[,\s]+/).map((name) => name.trim()).filter(Boolean);
}

export function validateClusterSettings(draft: ClusterSettingsDraft): ClusterSettingsValidation {
  const errors: Record<string, string> = {};
  let containerRuntime: ContainerRuntimePatch | null = null;

  if (draft.containerEnabled) {
    const prefixes = draft.allowedImagePrefixes.split(/\r?\n/).map((prefix) => prefix.trim()).filter(Boolean);
    const binary = draft.binary.trim();
    const mpiPlugin = draft.mpiPlugin.trim();

    if (draft.runtimeType === "apptainer" && binary &&
      (binary.length > 128 || !binaryPattern.test(binary))) {
      errors.binary = "Use at most 128 characters from the allowed executable path character set.";
    }
    if (mpiPlugin && !mpiPluginPattern.test(mpiPlugin)) {
      errors.mpiPlugin = "Use lowercase letters, digits, and underscores only.";
    }
    prefixes.forEach((prefix, index) => {
      if (prefix.length > 512 || !imagePrefixPattern.test(prefix) || prefix.split("/").includes("..")) {
        errors.allowedImagePrefixes = `Prefix ${String(index + 1)} must be a valid docker://, oras://, or absolute-path prefix up to 512 characters.`;
      }
    });
    if (Object.keys(errors).length === 0) {
      containerRuntime = {
        type: draft.runtimeType,
        ...(draft.runtimeType === "apptainer" && binary ? { binary } : {}),
        allowed_image_prefixes: prefixes,
        require_digest: draft.requireDigest,
        slurm_in_container: draft.slurmInContainer,
        ...(mpiPlugin ? { mpi_plugin: mpiPlugin } : {}),
      };
    }
  }

  const softwareModules: SoftwareModule[] = [];
  const seen = new Set<string>();
  draft.softwareModules.forEach((row, index) => {
    const rowPath = `softwareModules.${row.id || String(index)}`;
    const name = row.name.trim();
    const version = row.version.trim();
    const modules = parseModuleNames(row.modules);
    if (!name && !version && modules.length === 0) return;

    if (!softwareNamePattern.test(name)) {
      errors[`${rowPath}.name`] = "Name must match the allowed software name pattern.";
    }
    if (version && !softwareNamePattern.test(version)) {
      errors[`${rowPath}.version`] = "Version must match the allowed software name pattern.";
    }
    if (modules.length < 1 || modules.length > 16) {
      errors[`${rowPath}.modules`] = "Enter between 1 and 16 module names.";
    } else if (modules.some((module) => !moduleNamePattern.test(module))) {
      errors[`${rowPath}.modules`] = "Each module must match the allowed module name pattern.";
    }
    const pair = `${name}\u0000${version}`;
    if (seen.has(pair)) {
      errors[`${rowPath}.name`] = "This name and version pair is already listed.";
    }
    seen.add(pair);
    softwareModules.push({ name, ...(version ? { version } : {}), modules });
  });

  if (Object.keys(errors).length > 0) return { valid: false, errors };
  return {
    valid: true,
    payload: { container_runtime: containerRuntime, software_modules: softwareModules },
    errors: {},
  };
}
