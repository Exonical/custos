export function sanitizeBffPath(pathname: string): string | null {
  const prefix = "/api/bff/";
  if (!pathname.startsWith(prefix)) return null;
  const raw = pathname.slice(prefix.length);
  if (raw.length === 0) return null;

  const segments = raw.split("/");
  const safe: string[] = [];
  for (const segment of segments) {
    if (!segment || /%(?:2f|5c|00)/i.test(segment)) return null;
    let decoded: string;
    try {
      decoded = decodeURIComponent(segment);
    } catch {
      return null;
    }
    if (
      !decoded ||
      decoded === "." ||
      decoded === ".." ||
      /%(?:2f|5c|00)/i.test(decoded) ||
      decoded.includes("/") ||
      decoded.includes("\\") ||
      decoded.includes("\u0000")
    ) {
      return null;
    }
    safe.push(encodeURIComponent(decoded));
  }
  return safe.join("/");
}

const REQUEST_HEADERS = ["accept", "content-type", "idempotency-key", "if-match"] as const;
const HOP_BY_HOP = new Set([
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
  "set-cookie",
]);

export function allowedRequestHeaders(headers: Headers): Headers {
  const allowed = new Headers();
  for (const name of REQUEST_HEADERS) {
    const value = headers.get(name);
    if (value !== null) allowed.set(name, value);
  }
  return allowed;
}

export function copyResponseHeaders(source: Headers): Headers {
  const headers = new Headers();
  const connectionHeaders = new Set(
    (source.get("connection") ?? "")
      .split(",")
      .map((name) => name.trim().toLowerCase())
      .filter(Boolean),
  );
  source.forEach((value, name) => {
    const normalized = name.toLowerCase();
    if (!HOP_BY_HOP.has(normalized) && !connectionHeaders.has(normalized)) headers.set(name, value);
  });
  headers.set("cache-control", "no-store");
  return headers;
}
