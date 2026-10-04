"use client";

import dynamic from "next/dynamic";
import { usePathname, useRouter } from "next/navigation";
import { useState } from "react";
import { WorkflowGraph } from "@/components/workflows/workflow-graph";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { stringifyWorkflowYaml } from "@/lib/workflow/yaml";
import type { NormalizedWorkflowSpec } from "@/lib/workflow/normalize";

const WorkflowYamlEditor = dynamic(() => import("@/components/workflows/yaml-editor").then((module) => module.WorkflowYamlEditor), {
  ssr: false,
  loading: () => <div className="grid h-[34rem] place-items-center border border-border bg-card font-mono text-xs text-muted-foreground">Loading editor…</div>,
});

const tabs = ["graph", "yaml", "parameters"] as const;
export type WorkflowVersionTab = (typeof tabs)[number];

export function WorkflowVersionTabs({
  spec,
  layout,
  selectedTab,
  downloadContext,
}: {
  spec: NormalizedWorkflowSpec;
  layout?: unknown;
  selectedTab: WorkflowVersionTab;
  downloadContext?: { tenant: string; workflow: string; version: string };
}) {
  const router = useRouter();
  const pathname = usePathname();
  const [taskName, setTaskName] = useState<string | null>(null);
  function changeTab(value: string | null) {
    if (!value || !tabs.includes(value as WorkflowVersionTab)) return;
    const params = new URLSearchParams(window.location.search);
    if (value === "graph") params.delete("tab");
    else params.set("tab", value);
    router.replace(`${pathname}${params.size ? `?${params.toString()}` : ""}`, { scroll: false });
  }

  const parameters = Object.entries(spec.spec.parameters);
  return (
    <Tabs value={selectedTab} onValueChange={changeTab}>
      <TabsList variant="line" className="h-8 w-full justify-start border-b border-border px-3">
        <TabsTrigger value="graph">Graph</TabsTrigger>
        <TabsTrigger value="yaml">YAML</TabsTrigger>
        <TabsTrigger value="parameters">Parameters</TabsTrigger>
      </TabsList>
      <TabsContent value="graph" className="min-w-0 p-3">
        <WorkflowGraph spec={spec} layout={layout} selectedTask={taskName} onSelectTask={setTaskName} taskDownload={downloadContext} />
      </TabsContent>
      <TabsContent value="yaml" className="min-w-0 p-3">
        <WorkflowYamlEditor value={stringifyWorkflowYaml(spec)} />
      </TabsContent>
      <TabsContent value="parameters" className="min-w-0">
        <div className="overflow-hidden bg-card">
          <Table containerClassName="max-h-[60vh]">
            <caption className="sr-only">Workflow runtime parameters</caption>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Name</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Required</TableHead>
                <TableHead>Default</TableHead>
                <TableHead>Constraints</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {parameters.length === 0 ? (
                <TableRow><TableCell colSpan={5} className="py-10 text-center">
                  <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No parameters</p>
                  <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
                </TableCell></TableRow>
              ) : parameters.map(([name, parameter]) => (
                <TableRow key={name}>
                  <TableCell className="font-mono text-xs">{name}</TableCell>
                  <TableCell className="font-mono text-xs">{parameter.type}</TableCell>
                  <TableCell className="font-mono text-xs">{parameter.required ? "yes" : "no"}</TableCell>
                  <TableCell className="font-mono text-xs">{parameter.default === undefined ? "—" : JSON.stringify(parameter.default)}</TableCell>
                  <TableCell className="font-mono text-xs">{[
                    parameter.minimum !== undefined ? `min ${String(parameter.minimum)}` : undefined,
                    parameter.maximum !== undefined ? `max ${String(parameter.maximum)}` : undefined,
                    parameter.pattern ? `pattern ${parameter.pattern}` : undefined,
                    parameter.enum ? `enum ${parameter.enum.join(", ")}` : undefined,
                  ].filter(Boolean).join(" · ") || "—"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </TabsContent>
    </Tabs>
  );
}
