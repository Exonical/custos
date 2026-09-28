import Link from "next/link";
import { Button } from "@/components/ui/button";

export default function ForbiddenPage() {
  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center gap-4 px-6">
      <p className="text-sm font-semibold uppercase tracking-wide text-rose-700">403 · FORBIDDEN</p>
      <h1 className="text-3xl font-bold">You do not have access</h1>
      <p className="text-slate-600">Ask a tenant administrator if you believe this is a mistake.</p>
      <Button asChild className="w-fit"><Link href="/">Return to Custos</Link></Button>
    </main>
  );
}
