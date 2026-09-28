"use client";

import Link from "next/link";
import { Button } from "@/components/ui/button";

export default function ErrorPage({ reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center gap-4 px-6">
      <p className="text-sm font-semibold uppercase tracking-wide text-rose-700">500 · INTERNAL_ERROR</p>
      <h1 className="text-3xl font-bold">Something went wrong</h1>
      <p className="text-slate-600">Custos could not complete this request. No upstream details are shown.</p>
      <div className="flex gap-3">
        <Button type="button" onClick={() => { reset(); }}>Try again</Button>
        <Button asChild variant="outline"><Link href="/">Return to Custos</Link></Button>
      </div>
    </main>
  );
}
