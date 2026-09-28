import type { DefaultSession } from "next-auth";

declare module "next-auth" {
  interface Session {
    user: { sub: string } & DefaultSession["user"];
    error?: string;
  }
}

declare module "next-auth/jwt" {
  interface JWT {
    access_token?: string;
    refresh_token?: string;
    expires_at?: number;
    error?: string;
  }
}
