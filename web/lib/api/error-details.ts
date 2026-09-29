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
