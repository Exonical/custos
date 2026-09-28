import { Skeleton } from "@/components/ui/skeleton";

export default function JobsLoading() {
  return (
    <section className="space-y-5" aria-busy="true">
      <p className="sr-only">Loading jobs</p>
      <Skeleton className="h-8 w-40" />
      <Skeleton className="h-16" />
      <Skeleton className="h-96" />
    </section>
  );
}
