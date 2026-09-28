import type { Account, Profile, Session } from "next-auth";
import type { JWT } from "next-auth/jwt";
import { refreshSingleFlight } from "@/lib/security/refresh-singleflight";

export const REFRESH_BEFORE_MS = 60_000;

export function isUsableAuthState(session: Session | null, token: JWT | null): boolean {
  return Boolean(
    session &&
    !session.error &&
    typeof session.user.sub === "string" &&
    token &&
    !token.error &&
    token.sub === session.user.sub &&
    typeof token.access_token === "string",
  );
}

export type TokenRefreshResponse = Readonly<{
  access_token?: string;
  refresh_token?: string;
  expiresIn(): number | undefined;
}>;

export type TokenRefresh = (refreshToken: string) => Promise<TokenRefreshResponse>;
export type JwtCallbackInput = Readonly<{ token: JWT; account: Account | null; profile?: Profile }>;

async function refreshWithOidc(refreshToken: string): Promise<TokenRefreshResponse> {
  const { refreshAccessToken } = await import("@/lib/auth/oidc");
  return refreshAccessToken(refreshToken);
}

function withoutTokens(token: JWT): JWT {
  const next: JWT = { ...token, error: "RefreshTokenError" };
  delete next.access_token;
  delete next.refresh_token;
  delete next.expires_at;
  delete next["id_token"];
  return next;
}

export async function jwtCallback(
  { token, account, profile }: JwtCallbackInput,
  options: Readonly<{ refresh?: TokenRefresh; now?: number; forceRefresh?: boolean }> = {},
): Promise<JWT> {
  const next = { ...token };
  delete next["id_token"];

  if (account) {
    const accessToken = account.access_token;
    const profileSubject = profile && typeof profile.sub === "string" ? profile.sub : undefined;
    const subject = typeof next.sub === "string" ? next.sub : profileSubject;
    if (typeof accessToken !== "string" || !subject) return withoutTokens(next);
    next.sub = subject;
    next.access_token = accessToken;
    next.refresh_token = typeof account.refresh_token === "string" ? account.refresh_token : undefined;
    next.expires_at = typeof account.expires_at === "number"
      ? account.expires_at * 1000
      : Date.now() + (typeof account.expires_in === "number" ? account.expires_in : 300) * 1000;
    delete next.error;
    return next;
  }

  if (next.error === "RefreshTokenError") return withoutTokens(next);
  if (typeof next.access_token !== "string" || typeof next.sub !== "string") return withoutTokens(next);

  const now = options.now ?? Date.now();
  const expiresAt = typeof next.expires_at === "number" ? next.expires_at : 0;
  if (!options.forceRefresh && expiresAt - now > REFRESH_BEFORE_MS) return next;
  if (typeof next.refresh_token !== "string" || next.refresh_token.length === 0) return withoutTokens(next);

  const refreshToken = next.refresh_token;
  try {
    const updated = await refreshSingleFlight(refreshToken, () => (options.refresh ?? refreshWithOidc)(refreshToken));
    if (typeof updated.access_token !== "string") return withoutTokens(next);
    next.access_token = updated.access_token;
    next.refresh_token = typeof updated.refresh_token === "string" ? updated.refresh_token : refreshToken;
    next.expires_at = Date.now() + (updated.expiresIn() ?? 300) * 1000;
    delete next.error;
    return next;
  } catch {
    return withoutTokens(next);
  }
}

export function sessionCallback(session: Session, token: JWT): Session {
  const result: Session = {
    expires: session.expires,
    user: {
      sub: typeof token.sub === "string" ? token.sub : "",
      name: typeof token.name === "string" ? token.name : null,
      email: typeof token.email === "string" ? token.email : null,
    },
  };
  if (typeof token.error === "string") result.error = token.error;
  return result;
}
