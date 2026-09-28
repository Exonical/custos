import "server-only";
import { cookies, headers } from "next/headers";
import { redirect } from "next/navigation";
import { auth } from "@/auth";
import { getConfig } from "@/lib/config";
import { isUsableAuthState } from "@/lib/auth/callbacks";
import { getRequestSession, requestAuthToken } from "@/lib/session/request";
import type { ServerSession } from "@/lib/session/model";

export async function getServerSession(): Promise<ServerSession | null> {
  try {
    const authSession = await auth();
    if (!authSession || authSession.error || typeof authSession.user.sub !== "string") return null;

    const config = getConfig();
    const requestHeaders = new Headers(await headers());
    const cookieHeader = (await cookies()).getAll().map(({ name, value }) => `${name}=${value}`).join("; ");
    requestHeaders.set("cookie", cookieHeader);
    const request = new Request(config.publicOrigin, { headers: requestHeaders });
    const token = await requestAuthToken(request);
    if (!isUsableAuthState(authSession, token)) return null;
    const result = await getRequestSession(request, { refresh: false });
    if (!result.session || result.session.subject !== authSession.user.sub) return null;
    return {
      subject: result.session.subject,
      accessToken: result.session.accessToken,
      csrfToken: result.session.csrfToken,
    };
  } catch {
    return null;
  }
}

export async function requireServerSession(returnTo: string): Promise<ServerSession> {
  const session = await getServerSession();
  if (!session) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
  return session;
}
