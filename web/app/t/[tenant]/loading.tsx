import { Skeleton } from "@/components/ui/skeleton";

export default function TenantLoading() {
  return (
    <section className="space-y-5" aria-busy="true">
      <p className="sr-only">Loading tenant workspace</p>
      <Skeleton className="h-8 w-56" />
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {Array.from({ length: 6 }, (_, index) => <Skeleton key={index} className="h-24" />)}
      </div>
      <Skeleton className="h-72" />
    </section>
  );
}
