import { Button } from "@/components/ui/button";

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
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center px-5 py-12">
      <div className="space-y-4 border border-border bg-card p-6">
        <p className="font-mono text-[10px] uppercase tracking-[0.14em] text-primary">{"// Sign-in error"}</p>
        <h1 className="font-mono text-lg font-semibold uppercase tracking-[0.12em]">We could not sign you in</h1>
        <p className="text-sm text-muted-foreground">{messages[code]}</p>
        <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">{code}</p>
        <Button className="w-fit" nativeButton={false} render={<a href="/auth/login">Try again</a>} />
      </div>
    </main>
  );
}
