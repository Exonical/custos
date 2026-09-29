export function ApiAccessDenied({ view }: { view: string }) {
  return (
    <section role="alert" className="max-w-3xl space-y-3 border border-destructive/35 bg-card p-5">
      <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-destructive">403 // Forbidden</p>
      <h1 className="font-mono text-lg font-semibold uppercase tracking-[0.12em]">You don&apos;t have access to this view</h1>
      <p className="text-sm text-muted-foreground">{view} is restricted. Ask a tenant administrator for access.</p>
    </section>
  );
}
