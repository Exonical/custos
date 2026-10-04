import "server-only";
import { readFileSync } from "node:fs";
import { Agent, fetch as undiciFetch } from "undici";

function isRequestInput(input: RequestInfo | URL): input is Request {
  return typeof input === "object"
    && "url" in input && "method" in input && "headers" in input;
}

export function createSafeFetch(caFile?: string, timeoutMs = 30_000): typeof fetch {
  const dispatcher = caFile
    ? new Agent({ connect: { ca: readFileSync(caFile) } })
    : undefined;

  return async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = isRequestInput(input) ? input : undefined;
    const target = request?.url ?? input;
    const requestOptions: RequestInit = request ? {
      method: request.method,
      headers: request.headers,
      body: request.body,
      redirect: request.redirect,
      cache: request.cache,
      credentials: request.credentials,
      integrity: request.integrity,
      keepalive: request.keepalive,
      mode: request.mode,
      referrer: request.referrer,
      referrerPolicy: request.referrerPolicy,
      ...(request.body ? { duplex: "half" } : {}),
    } : {};
    const timeoutSignal = AbortSignal.timeout(timeoutMs);
    const requestSignal = request?.signal;
    const signal = init?.signal ?? (requestSignal
      ? AbortSignal.any([requestSignal, timeoutSignal])
      : timeoutSignal);
    const options = { ...requestOptions, ...init, signal };
    if (!dispatcher) return fetch(target, options);
    const undiciOptions = { ...options, dispatcher } as Parameters<typeof undiciFetch>[1];
    return (await undiciFetch(target as Parameters<typeof undiciFetch>[0], undiciOptions)) as unknown as Response;
  };
}
