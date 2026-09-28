import type { NextRequest } from "next/server";

export function getCookieValue(request: Request | NextRequest, name: string): string | undefined {
  if ("cookies" in request) {
    return request.cookies.get(name)?.value;
  }
  const header = request.headers.get("cookie");
  if (!header) return undefined;
  for (const part of header.split(";")) {
    const index = part.indexOf("=");
    if (index < 0) continue;
    if (part.slice(0, index).trim() === name) return part.slice(index + 1).trim();
  }
  return undefined;
}
