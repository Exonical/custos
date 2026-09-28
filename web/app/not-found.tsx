import Link from "next/link";
import { Button } from "@/components/ui/button";

export default function NotFoundPage() {
  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center px-5 py-12">
      <div className="space-y-4 border border-border bg-card p-6">
        <p className="font-mono text-[10px] uppercase tracking-[0.14em] text-primary">404 // Not found</p>
        <h1 className="font-mono text-lg font-semibold uppercase tracking-[0.12em]">This page is unavailable</h1>
        <p className="text-sm text-muted-foreground">The resource may have been removed or may not be visible to your account.</p>
        <Button className="w-fit" nativeButton={false} render={<Link href="/">Return to Custos</Link>} />
      </div>
    </main>
  );
}
