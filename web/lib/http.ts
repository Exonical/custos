import "server-only";
import { readFileSync } from "node:fs";
import { Agent, fetch as undiciFetch } from "undici";

export function createSafeFetch(caFile?: string, timeoutMs = 30_000): typeof fetch {
  const dispatcher = caFile
    ? new Agent({ connect: { ca: readFileSync(caFile) } })
    : undefined;

  return async (input: RequestInfo | URL, init?: RequestInit) => {
    const signal = init?.signal ?? AbortSignal.timeout(timeoutMs);
    const options = { ...init, signal };
    if (!dispatcher) return fetch(input, options);
    const undiciOptions = { ...options, dispatcher } as Parameters<typeof undiciFetch>[1];
    return (await undiciFetch(input as Parameters<typeof undiciFetch>[0], undiciOptions)) as unknown as Response;
  };
}
