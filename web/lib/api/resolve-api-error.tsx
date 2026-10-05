import type { ReactElement } from "react";
import { notFound, redirect } from "next/navigation";
import { ApiAccessDenied } from "@/components/api-access-denied";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { toApiError } from "@/lib/api/client";

export interface ApiErrorResult {
  error?: unknown;
  response: { status: number };
}

export interface ResolveApiErrorOptions {
  /** Path to come back to after re-authenticating on 401. */
  returnTo: string;
  /** Label for the <ApiAccessDenied/> view on 403. Without it, 403 renders the generic <ApiErrorNotice/>. */
  view?: string;
  /**
   * What a 404 does: "page" (default) renders the Next.js not-found page,
   * "notice" renders the generic <ApiErrorNotice/>, "skip" returns null so the caller can carry on.
   */
  notFound?: "page" | "notice" | "skip";
}

export function loginRedirectIfUnauthorized(status: number, returnTo: string): void {
  if (status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
}

/**
 * Maps a failed API response to the page outcome shared by every server page:
 * 401 redirects to login, 404 renders not-found, 403 renders <ApiAccessDenied/>,
 * anything else renders <ApiErrorNotice/>.
 */
export function resolveApiError(result: ApiErrorResult, options: ResolveApiErrorOptions & { notFound: "skip" }): ReactElement | null;
export function resolveApiError(result: ApiErrorResult, options: ResolveApiErrorOptions & { notFound?: "page" | "notice" }): ReactElement;
export function resolveApiError(result: ApiErrorResult, options: ResolveApiErrorOptions): ReactElement | null {
  const { status } = result.response;
  loginRedirectIfUnauthorized(status, options.returnTo);
  if (status === 404) {
    const onNotFound = options.notFound ?? "page";
    if (onNotFound === "page") notFound();
    if (onNotFound === "skip") return null;
  }
  if (status === 403 && options.view !== undefined) return <ApiAccessDenied view={options.view} />;
  return <ApiErrorNotice error={toApiError(result.error, status)} />;
}
