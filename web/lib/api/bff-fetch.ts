import { detailText, extractErrorEnvelope } from "@/lib/api/error-details";

export function errorText(value: unknown, status: number, action: string): string {
  const error = extractErrorEnvelope(value);
  const code = error.code ?? `HTTP_${String(status)}`;
  if (status === 403) return `You do not have permission to ${action} in this project.`;
  if (status === 409 && code === "WORKFLOW_NAME_TAKEN") return "A workflow with this name already exists in the project.";
  const details = error.details ? detailText(error.details) : "";
  if (details) return details;
  if (error.message) return error.message;
  return `Request rejected: ${code}`;
}

export async function postJson(url: string, body: unknown, csrfToken: string) {
  return sendJson("POST", url, body, csrfToken);
}

export async function sendJson(
  method: "POST" | "PATCH" | "PUT",
  url: string,
  body: unknown,
  csrfToken: string,
  extraHeaders: Record<string, string> = {},
) {
  const response = await fetch(url, {
    method,
    credentials: "same-origin",
    cache: "no-store",
    headers: { Accept: "application/json", "Content-Type": "application/json", "X-CSRF-Token": csrfToken, ...extraHeaders },
    body: JSON.stringify(body),
  });
  const payload: unknown = await response.json().catch(() => null);
  return { status: response.status, payload };
}

export async function sendText(
  method: "POST" | "PUT",
  url: string,
  body: string,
  contentType: string,
  csrfToken: string,
  extraHeaders: Record<string, string> = {},
) {
  const response = await fetch(url, {
    method,
    credentials: "same-origin",
    cache: "no-store",
    headers: {
      Accept: "application/json",
      "Content-Type": contentType,
      "X-CSRF-Token": csrfToken,
      ...extraHeaders,
    },
    body,
  });
  const payload: unknown = await response.json().catch(() => null);
  return { status: response.status, payload };
}
