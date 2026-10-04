export const SBATCH_IMPORT_MAX_FILES = 20;
export const SBATCH_IMPORT_MAX_SCRIPT_BYTES = 262_144;
export const SBATCH_IMPORT_MAX_BODY_BYTES = 2 * 1024 * 1024;

export type SbatchImportScript = { filename: string; content: string };
export type SbatchImportRequest = { name?: string; scripts: SbatchImportScript[] };

export type SbatchImportValidation =
  | { valid: true; request: SbatchImportRequest; bodyBytes: number; scriptBytes: number[] }
  | { valid: false; error: string; scriptBytes: number[] };

export function validateSbatchImport(name: string, scripts: SbatchImportScript[]): SbatchImportValidation {
  const trimmedName = name.trim();
  const scriptBytes = scripts.map(({ content }) => new TextEncoder().encode(content).byteLength);
  if (trimmedName.length > 256) {
    return { valid: false, error: "Workflow name must be 256 characters or fewer.", scriptBytes };
  }
  if (scripts.length < 1) {
    return { valid: false, error: "Choose at least one script to import.", scriptBytes };
  }
  if (scripts.length > SBATCH_IMPORT_MAX_FILES) {
    return { valid: false, error: `Import no more than ${String(SBATCH_IMPORT_MAX_FILES)} scripts at once.`, scriptBytes };
  }
  const oversizedIndex = scriptBytes.findIndex((size) => size > SBATCH_IMPORT_MAX_SCRIPT_BYTES);
  if (oversizedIndex >= 0) {
    return {
      valid: false,
      error: `${scripts[oversizedIndex]?.filename ?? "A selected file"} exceeds the 256 KiB per-file limit.`,
      scriptBytes,
    };
  }
  const request: SbatchImportRequest = {
    ...(trimmedName ? { name: trimmedName } : {}),
    scripts,
  };
  const bodyBytes = new TextEncoder().encode(JSON.stringify(request)).byteLength;
  if (bodyBytes > SBATCH_IMPORT_MAX_BODY_BYTES) {
    return { valid: false, error: "The JSON request exceeds 2 MiB. Import fewer files at once.", scriptBytes };
  }
  return { valid: true, request, bodyBytes, scriptBytes };
}
