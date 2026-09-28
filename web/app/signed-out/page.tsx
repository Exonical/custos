import Link from "next/link";

export default function SignedOutPage() {
  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col justify-center gap-5 px-6">
      <p className="text-sm font-semibold uppercase tracking-wide text-teal-700">Custos</p>
      <h1 className="text-3xl font-bold">You are signed out</h1>
      <p className="text-slate-600">Your browser session has been cleared.</p>
      <Link className="w-fit rounded-md bg-teal-700 px-4 py-2 text-white" href="/auth/login">
        Sign in again
      </Link>
    </main>
  );
}
