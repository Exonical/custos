export default function TenantLoading() {
  return (
    <section className="space-y-6" aria-busy="true">
      <p className="sr-only">Loading tenant workspace</p>
      <div className="h-10 w-56 animate-pulse rounded bg-slate-200" />
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {Array.from({ length: 6 }, (_, index) => <div key={index} className="h-24 animate-pulse rounded-xl bg-white shadow-sm" />)}
      </div>
      <div className="h-72 animate-pulse rounded-xl bg-white shadow-sm" />
    </section>
  );
}
