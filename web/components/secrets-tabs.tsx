"use client";

import { usePathname, useRouter } from "next/navigation";
import { useState, type ReactNode } from "react";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

export type SecretTab = "connectors" | "references";

export function SecretsTabs({
  selectedTab,
  connectorsContent,
  referencesContent,
}: {
  selectedTab: SecretTab;
  connectorsContent: ReactNode;
  referencesContent: ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const [activeTab, setActiveTab] = useState<SecretTab>(selectedTab);

  function changeTab(value: string | null) {
    if (value !== "connectors" && value !== "references") return;
    setActiveTab(value);
    const params = new URLSearchParams(window.location.search);
    if (value === "connectors") params.delete("tab");
    else params.set("tab", value);
    router.replace(`${pathname}${params.size ? `?${params.toString()}` : ""}`, { scroll: false });
  }

  return (
    <section className="border border-border bg-card">
      <Tabs value={activeTab} onValueChange={changeTab}>
        <TabsList variant="line" className="h-8 w-full justify-start border-b border-border px-3">
          <TabsTrigger value="connectors">Connectors</TabsTrigger>
          <TabsTrigger value="references">References</TabsTrigger>
        </TabsList>
        <TabsContent value="connectors" className="p-4">{connectorsContent}</TabsContent>
        <TabsContent value="references" className="p-4">{referencesContent}</TabsContent>
      </Tabs>
      <p className="border-t border-border px-4 py-3 font-mono text-[9px] uppercase tracking-[0.08em] text-muted-foreground">Credentials and secret values are never displayed.</p>
    </section>
  );
}
