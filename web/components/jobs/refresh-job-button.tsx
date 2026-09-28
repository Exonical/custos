"use client";

import { RefreshCw } from "lucide-react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/button";

export function RefreshJobButton({ size = "icon" }: { size?: "icon" | "icon-sm" }) {
  const router = useRouter();
  return (
    <Button type="button" variant="outline" size={size} aria-label="Refresh" onClick={() => { router.refresh(); }}>
      <RefreshCw aria-hidden="true" className="size-4" />
    </Button>
  );
}
