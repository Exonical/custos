import Link from "next/link";
import { Button } from "@/components/ui/button";

export default function ForbiddenPage() {
  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center px-5 py-12">
      <div className="space-y-4 border border-border bg-card p-6">
        <p className="font-mono text-[10px] uppercase tracking-[0.14em] text-destructive">403 // Forbidden</p>
        <h1 className="font-mono text-lg font-semibold uppercase tracking-[0.12em]">You do not have access</h1>
        <p className="text-sm text-muted-foreground">Ask a tenant administrator if you believe this is a mistake.</p>
        <Button className="w-fit" nativeButton={false} render={<Link href="/">Return to Custos</Link>} />
      </div>
    </main>
  );
}
