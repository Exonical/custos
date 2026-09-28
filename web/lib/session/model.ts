export type ServerSession = Readonly<{
  subject: string;
  accessToken: string;
  csrfToken: string;
}>;

export type RequestSession = ServerSession & Readonly<{
  refreshToken: string | null;
}>;

export type RequestSessionResult = Readonly<{
  session: RequestSession | null;
  updatedCookie: string | null;
  clearSessionCookie: boolean;
}>;
