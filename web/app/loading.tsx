import { Skeleton } from "@/components/ui/skeleton";

export default function LoadingPage() {
  return (
    <main className="mx-auto flex min-h-screen max-w-5xl flex-col gap-6 px-5 py-12" aria-busy="true">
      <p className="sr-only">Loading Custos</p>
      <Skeleton className="h-7 w-48" />
      <div className="grid gap-3 sm:grid-cols-3">
        {Array.from({ length: 3 }, (_, index) => <Skeleton key={index} className="h-28" />)}
      </div>
      <Skeleton className="h-64" />
    </main>
  );
}
