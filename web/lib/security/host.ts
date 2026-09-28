function normalizedHost(value: string | null): string | null {
  if (!value || value.includes(",")) return null;
  try {
    const parsed = new URL(`http://${value}`);
    if (parsed.username || parsed.password || parsed.pathname !== "/" || parsed.search || parsed.hash) return null;
    return parsed.host.toLowerCase();
  } catch {
    return null;
  }
}

export function requestHostMatchesOrigin(request: Request, publicOrigin: string): boolean {
  const expected = normalizedHost(new URL(publicOrigin).host);
  const requestHost = normalizedHost(request.headers.get("host"));
  const forwardedHost = request.headers.get("x-forwarded-host");
  return Boolean(
    expected &&
    requestHost === expected &&
    (forwardedHost === null || normalizedHost(forwardedHost) === expected),
  );
}
