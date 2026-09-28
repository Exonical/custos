export default function JobsLoading() {
  return (
    <section className="space-y-5" aria-busy="true">
      <p className="sr-only">Loading jobs</p>
      <div className="h-10 w-40 animate-pulse rounded bg-slate-200" />
      <div className="h-16 animate-pulse rounded-xl bg-white shadow-sm" />
      <div className="h-96 animate-pulse rounded-xl bg-white shadow-sm" />
    </section>
  );
}
