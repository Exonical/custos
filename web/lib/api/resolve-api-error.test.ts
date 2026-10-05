import { describe, expect, it, vi } from "vitest";
import { isValidElement, type ReactElement } from "react";
import { ApiAccessDenied } from "@/components/api-access-denied";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { ApiError } from "@/lib/api/client";
import { loginRedirectIfUnauthorized, resolveApiError } from "@/lib/api/resolve-api-error";

vi.mock("next/navigation", () => ({
  redirect: vi.fn((url: string) => {
    throw new Error(`redirect:${url}`);
  }),
  notFound: vi.fn(() => {
    throw new Error("notFound");
  }),
}));

const returnTo = "/t/acme/workflows";
const failed = (status: number, error?: unknown) => ({ error, response: { status } });

function element(value: ReactElement | null): ReactElement<Record<string, unknown>> {
  if (!isValidElement<Record<string, unknown>>(value)) throw new Error("expected a React element");
  return value;
}

describe("resolveApiError", () => {
  it("redirects to login on 401", () => {
    expect(() => resolveApiError(failed(401), { returnTo, view: "Workflows" })).toThrow(
      `redirect:/auth/login?returnTo=${encodeURIComponent(returnTo)}`,
    );
  });

  it("renders the not-found page on 404 by default", () => {
    expect(() => resolveApiError(failed(404), { returnTo, view: "Workflows" })).toThrow("notFound");
  });

  it("renders a notice on 404 when notFound is 'notice'", () => {
    const result = element(resolveApiError(failed(404), { returnTo, notFound: "notice" }));
    expect(result.type).toBe(ApiErrorNotice);
    expect((result.props.error as ApiError).status).toBe(404);
  });

  it("returns null on 404 when notFound is 'skip'", () => {
    expect(resolveApiError(failed(404), { returnTo, view: "Workflow versions", notFound: "skip" })).toBeNull();
  });

  it("still fails non-404 errors when notFound is 'skip'", () => {
    const result = element(resolveApiError(failed(403), { returnTo, view: "Workflow versions", notFound: "skip" }));
    expect(result.type).toBe(ApiAccessDenied);
  });

  it("renders ApiAccessDenied with the view label on 403", () => {
    const result = element(resolveApiError(failed(403), { returnTo, view: "Workflows" }));
    expect(result.type).toBe(ApiAccessDenied);
    expect(result.props.view).toBe("Workflows");
  });

  it("renders the generic notice on 403 without a view label", () => {
    const result = element(resolveApiError(failed(403), { returnTo }));
    expect(result.type).toBe(ApiErrorNotice);
    expect((result.props.error as ApiError).status).toBe(403);
  });

  it("renders ApiErrorNotice with the parsed error envelope otherwise", () => {
    const body = { error: { code: "BACKEND_DOWN", request_id: "req-1" } };
    const result = element(resolveApiError(failed(503, body), { returnTo, view: "Workflows" }));
    expect(result.type).toBe(ApiErrorNotice);
    const error = result.props.error as ApiError;
    expect(error).toBeInstanceOf(ApiError);
    expect([error.status, error.code, error.requestId]).toEqual([503, "BACKEND_DOWN", "req-1"]);
  });

  it("falls back to UPSTREAM_ERROR when the response carries no error body", () => {
    const error = element(resolveApiError(failed(500), { returnTo })).props.error as ApiError;
    expect([error.status, error.code, error.requestId]).toEqual([500, "UPSTREAM_ERROR", null]);
  });
});

describe("loginRedirectIfUnauthorized", () => {
  it("redirects only on 401", () => {
    expect(() => {
      loginRedirectIfUnauthorized(401, returnTo);
    }).toThrow(`redirect:/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    for (const status of [200, 403, 404, 500]) {
      expect(() => {
        loginRedirectIfUnauthorized(status, returnTo);
      }).not.toThrow();
    }
  });
});
