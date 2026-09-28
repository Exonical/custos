import { Button } from "@/components/ui/button";

export default function SignedOutPage() {
  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center px-5 py-12">
      <div className="space-y-4 border border-border bg-card p-6">
        <p className="font-mono text-[10px] uppercase tracking-[0.14em] text-primary">CUSTOS // Session ended</p>
        <h1 className="font-mono text-lg font-semibold uppercase tracking-[0.12em]">You are signed out</h1>
        <p className="text-sm text-muted-foreground">Your browser session has been cleared.</p>
        <Button className="w-fit" nativeButton={false} render={<a href="/auth/login">Sign in again</a>} />
      </div>
    </main>
  );
}
