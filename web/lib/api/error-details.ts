// Wire shape of a backend apperr error response body.
export type ApiErrorEnvelope = { error?: { code?: unknown; message?: unknown; details?: unknown } };

// Narrowed fields of an apperr error; absent when missing or mistyped.
export type ApiError = { code?: string; message?: string; details?: unknown[] };

export function extractErrorEnvelope(value: unknown): ApiError {
  const error = value && typeof value === "object" ? (value as ApiErrorEnvelope).error : undefined;
  if (!error || typeof error !== "object") return {};
  const result: ApiError = {};
  if (typeof error.code === "string") result.code = error.code;
  if (typeof error.message === "string") result.message = error.message;
  if (Array.isArray(error.details)) result.details = error.details as unknown[];
  return result;
}

// Error.details entries are `{field, reason}` (apperr.Detail); render them
// as "field: reason" without trusting the shape of untyped JSON.
export function detailText(details: readonly unknown[]): string {
  return details.flatMap((detail) => {
    if (!detail || typeof detail !== "object") return [];
    const { field, reason } = detail as { field?: unknown; reason?: unknown };
    if (typeof reason !== "string" || !reason) return [];
    return [typeof field === "string" && field ? `${field}: ${reason}` : reason];
  }).join(" · ");
}
