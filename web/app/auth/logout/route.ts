import { NextRequest, NextResponse } from "next/server";
import { signOut } from "@/auth";
import { createEndSessionUrl, revokeRefreshToken } from "@/lib/auth/oidc";
import { getConfig } from "@/lib/config";
import { checkCsrf } from "@/lib/security/csrf";
import { clearAuthCookies } from "@/lib/session/auth-cookie";
import { getRequestSession } from "@/lib/session/request";

export async function POST(request: NextRequest): Promise<NextResponse> {
  const config = getConfig();
  const result = await getRequestSession(request, { refresh: false });
  if (!result.session) {
    const response = NextResponse.json(
      { error: { code: "UNAUTHENTICATED" } },
      { status: 401, headers: { "Cache-Control": "no-store" } },
    );
    if (result.clearSessionCookie) clearAuthCookies(response, request, config);
    return response;
  }
  if (!checkCsrf(request, result.session, config)) {
    return NextResponse.json(
      { error: { code: "CSRF_REJECTED" } },
      { status: 403, headers: { "Cache-Control": "no-store" } },
    );
  }

  await revokeRefreshToken(result.session.refreshToken);
  await signOut({ redirect: false, redirectTo: "/signed-out" }).catch(() => null);
  const endSessionUrl = await createEndSessionUrl().catch(() => new URL("/signed-out", config.publicOrigin));
  const response = NextResponse.redirect(endSessionUrl, 303);
  clearAuthCookies(response, request, config);
  return response;
}
