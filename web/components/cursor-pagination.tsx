"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { Button } from "@/components/ui/button";

export function CursorPagination({
  pathname,
  query,
  cursor,
  nextCursor,
  countLabel,
}: {
  pathname: string;
  query: Record<string, string>;
  cursor: string;
  nextCursor: string | null;
  countLabel: string;
}) {
  const router = useRouter();
  const [previousCursors, setPreviousCursors] = useState<string[]>([]);

  function navigate(toCursor: string) {
    const params = new URLSearchParams(query);
    params.delete("cursor");
    if (toCursor) params.set("cursor", toCursor);
    router.push(`${pathname}${params.size ? `?${params.toString()}` : ""}`);
  }

  function next() {
    if (!nextCursor) return;
    setPreviousCursors((history) => [...history, cursor]);
    navigate(nextCursor);
  }

  function previous() {
    if (previousCursors.length === 0) return;
    const history = previousCursors.slice(0, -1);
    const previousCursor = previousCursors[previousCursors.length - 1] ?? "";
    setPreviousCursors(history);
    navigate(previousCursor);
  }

  return (
    <div className="flex items-center justify-between gap-3 border-t border-border px-3 py-3 font-mono text-[10px] uppercase tracking-[0.06em] text-muted-foreground">
      <span>{countLabel} · Page {previousCursors.length + 1}</span>
      <div className="flex items-center gap-2">
        <Button type="button" variant="outline" size="sm" aria-label="Previous page" disabled={previousCursors.length === 0} onClick={previous}>Previous</Button>
        <Button type="button" variant="outline" size="sm" aria-label="Next page" disabled={!nextCursor} onClick={next}>Next</Button>
      </div>
    </div>
  );
}
