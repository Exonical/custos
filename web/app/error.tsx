"use client";

import Link from "next/link";
import { Button } from "@/components/ui/button";

export default function ErrorPage({ reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center px-5 py-12">
      <div className="space-y-4 border border-border bg-card p-6">
        <p className="font-mono text-[10px] uppercase tracking-[0.14em] text-destructive">500 // Internal error</p>
        <h1 className="font-mono text-lg font-semibold uppercase tracking-[0.12em]">Something went wrong</h1>
        <p className="text-sm text-muted-foreground">Custos could not complete this request. No upstream details are shown.</p>
        <div className="flex flex-wrap gap-3">
          <Button type="button" onClick={() => { reset(); }}>Try again</Button>
          <Button variant="outline" nativeButton={false} render={<Link href="/">Return to Custos</Link>} />
        </div>
      </div>
    </main>
  );
}
