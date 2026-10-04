"use client";

import { usePathname, useRouter } from "next/navigation";
import type { Workflow } from "@/lib/api/client";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

const executionStates = ["PENDING", "VALIDATING", "QUEUED", "RUNNING", "SUCCEEDED", "FAILED", "PARTIAL_FAILURE", "CANCELING", "CANCELED"] as const;

export function ExecutionListFilters({ workflows, workflow, state, testRuns = "all", showWorkflow = true }: {
  workflows: Workflow[];
  workflow: string;
  state: string;
  testRuns?: "all" | "only" | "exclude";
  showWorkflow?: boolean;
}) {
  const router = useRouter();
  const pathname = usePathname();

  function update(name: "workflow" | "state" | "test", value: string) {
    const params = new URLSearchParams(window.location.search);
    params.delete("cursor");
    if (!value || value === "all") params.delete(name);
    else if (name === "test") params.set(name, value === "only" ? "true" : "false");
    else params.set(name, value);
    router.push(`${pathname}${params.size ? `?${params.toString()}` : ""}`);
  }

  return (
    <div className="flex flex-wrap items-end gap-3 border-y border-border py-3">
      {showWorkflow ? (
        <div className="grid gap-2">
          <Label htmlFor="execution-workflow-filter">Workflow</Label>
          <Select value={workflow || "all"} onValueChange={(value) => { if (typeof value === "string") update("workflow", value); }}>
            <SelectTrigger id="execution-workflow-filter" aria-label="Workflow" className="h-8 min-w-44">
              <SelectValue>{workflows.find((item) => item.id === workflow)?.name ?? "All workflows"}</SelectValue>
            </SelectTrigger>
            <SelectContent align="start">
              <SelectItem value="all">All workflows</SelectItem>
              {workflows.map((item) => <SelectItem key={item.id} value={item.id}>{item.name}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
      ) : null}
      <div className="grid gap-2">
        <Label htmlFor="execution-state-filter">State</Label>
        <Select value={state || "all"} onValueChange={(value) => { if (typeof value === "string") update("state", value); }}>
          <SelectTrigger id="execution-state-filter" aria-label="State" className="h-8 min-w-40">
            <SelectValue>{state || "All states"}</SelectValue>
          </SelectTrigger>
          <SelectContent align="start">
            <SelectItem value="all">All states</SelectItem>
            {executionStates.map((item) => <SelectItem key={item} value={item}>{item}</SelectItem>)}
          </SelectContent>
        </Select>
      </div>
      <div className="grid gap-2">
        <Label htmlFor="execution-test-filter">Test runs</Label>
        <Select value={testRuns} onValueChange={(value) => { if (typeof value === "string") update("test", value); }}>
          <SelectTrigger id="execution-test-filter" aria-label="Test runs" className="h-8 min-w-40">
            <SelectValue>{testRuns === "only" ? "Only test runs" : testRuns === "exclude" ? "Exclude test runs" : "All runs"}</SelectValue>
          </SelectTrigger>
          <SelectContent align="start">
            <SelectItem value="all">All runs</SelectItem>
            <SelectItem value="only">Only test runs</SelectItem>
            <SelectItem value="exclude">Exclude test runs</SelectItem>
          </SelectContent>
        </Select>
      </div>
    </div>
  );
}
