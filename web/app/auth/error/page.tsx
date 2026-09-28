import Link from "next/link";

const messages: Record<string, string> = {
  AccessDenied: "Your identity provider denied access to Custos.",
  Configuration: "Custos could not complete sign-in. Contact your administrator.",
  OAuthAccountNotLinked: "This identity is not linked to a Custos account.",
  OAuthCallback: "The sign-in response could not be verified.",
  OAuthSignin: "Custos could not start sign-in with the identity provider.",
  Verification: "The sign-in verification expired. Try again.",
  OIDC_CALLBACK_INVALID: "The sign-in request expired or could not be verified.",
  OIDC_CALLBACK_FAILED: "Custos could not complete sign-in.",
};

export default async function AuthErrorPage({ searchParams }: {
  searchParams: Promise<{ error?: string | string[]; code?: string | string[] }>;
}) {
  const params = await searchParams;
  const requested = typeof params.error === "string" ? params.error : params.code;
  const code = typeof requested === "string" && requested in messages ? requested : "Configuration";
  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center gap-5 px-6">
      <p className="text-sm font-semibold uppercase tracking-wide text-teal-700">Sign-in error</p>
      <h1 className="text-3xl font-bold">We could not sign you in</h1>
      <p className="text-slate-600">{messages[code]}</p>
      <p className="font-mono text-sm text-slate-500">{code}</p>
      <Link className="w-fit rounded-md bg-teal-700 px-4 py-2 text-white" href="/auth/login">
        Try again
      </Link>
    </main>
  );
}
