import { NextRequest, NextResponse } from "next/server";
import { signIn } from "@/auth";
import { getConfig } from "@/lib/config";
import { safeReturnTo } from "@/lib/security/return-to";

export async function GET(request: NextRequest): Promise<NextResponse> {
  const config = getConfig();
  const returnTo = safeReturnTo(request.nextUrl.searchParams.get("returnTo"));
  try {
    const authorizationUrl: unknown = await signIn("custos", { redirect: false, redirectTo: returnTo });
    if (typeof authorizationUrl !== "string") throw new Error("OIDC authorization URL is unavailable");
    const response = NextResponse.redirect(new URL(authorizationUrl));
    response.headers.set("Cache-Control", "no-store");
    return response;
  } catch {
    const response = NextResponse.redirect(new URL("/auth/error?error=OAuthSignin", config.publicOrigin));
    response.headers.set("Cache-Control", "no-store");
    return response;
  }
}
