import Link from "next/link";
import { Button } from "@/components/ui/button";

export default function NotFoundPage() {
  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center gap-4 px-6">
      <p className="text-sm font-semibold uppercase tracking-wide text-teal-700">404</p>
      <h1 className="text-3xl font-bold">This page is unavailable</h1>
      <p className="text-slate-600">The resource may have been removed or may not be visible to your account.</p>
      <Button asChild className="w-fit"><Link href="/">Return to Custos</Link></Button>
    </main>
  );
}
