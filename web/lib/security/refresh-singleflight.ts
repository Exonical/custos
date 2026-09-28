import { createHash } from "node:crypto";

const inFlight = new Map<string, Promise<unknown>>();

export async function refreshSingleFlight<T>(refreshToken: string, operation: () => Promise<T>): Promise<T> {
  const key = createHash("sha256").update(refreshToken, "utf8").digest("hex");
  const pending = inFlight.get(key);
  if (pending) return pending as Promise<T>;

  const request = Promise.resolve().then(operation);
  inFlight.set(key, request);
  try {
    return await request;
  } finally {
    if (inFlight.get(key) === request) inFlight.delete(key);
  }
}

export function resetRefreshFlightsForTest(): void {
  inFlight.clear();
}
