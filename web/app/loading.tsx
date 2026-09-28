export default function LoadingPage() {
  return (
    <main className="mx-auto flex min-h-screen max-w-5xl flex-col gap-6 px-6 py-12" aria-busy="true">
      <p className="sr-only">Loading Custos</p>
      <div className="h-8 w-48 animate-pulse rounded bg-slate-200" />
      <div className="grid gap-4 sm:grid-cols-3">
        {Array.from({ length: 3 }, (_, index) => <div key={index} className="h-28 animate-pulse rounded-xl bg-white shadow-sm" />)}
      </div>
      <div className="h-64 animate-pulse rounded-xl bg-white shadow-sm" />
    </main>
  );
}
