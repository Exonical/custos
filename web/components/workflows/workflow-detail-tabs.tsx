"use client";

import type { ReactNode } from "react";
import { usePathname, useRouter } from "next/navigation";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

export type WorkflowDetailTab = "versions" | "executions";

export function WorkflowDetailTabs({
  selectedTab,
  versionsContent,
  executionsContent,
}: {
  selectedTab: WorkflowDetailTab;
  versionsContent: ReactNode;
  executionsContent: ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  function changeTab(value: string | null) {
    if (value !== "versions" && value !== "executions") return;
    const params = new URLSearchParams(window.location.search);
    if (value === "versions") params.delete("tab");
    else params.set("tab", value);
    router.replace(`${pathname}${params.size ? `?${params.toString()}` : ""}`, { scroll: false });
  }
  return (
    <Tabs value={selectedTab} onValueChange={changeTab}>
      <TabsList variant="line" className="h-8 w-full justify-start border-b border-border px-3">
        <TabsTrigger value="versions">Versions</TabsTrigger>
        <TabsTrigger value="executions">Executions</TabsTrigger>
      </TabsList>
      <TabsContent value="versions" className="min-w-0">{versionsContent}</TabsContent>
      <TabsContent value="executions" className="min-w-0">{executionsContent}</TabsContent>
    </Tabs>
  );
}
