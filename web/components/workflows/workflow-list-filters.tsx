"use client";

import { usePathname, useRouter } from "next/navigation";
import type { Project } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

export function WorkflowListFilters({ projects, project, includeArchived }: {
  projects: Project[];
  project: string;
  includeArchived: boolean;
}) {
  const router = useRouter();
  const pathname = usePathname();

  function update(name: "project" | "state", value: string) {
    const params = new URLSearchParams(window.location.search);
    params.delete("cursor");
    if (value) params.set(name, value);
    else params.delete(name);
    router.push(`${pathname}${params.size ? `?${params.toString()}` : ""}`);
  }

  return (
    <div className="flex flex-wrap items-end gap-3 border-y border-border py-3">
      <div className="grid gap-2">
        <Label htmlFor="workflow-project-filter">Project</Label>
        <Select value={project || "all"} onValueChange={(value) => { if (typeof value === "string") update("project", value === "all" ? "" : value); }}>
          <SelectTrigger id="workflow-project-filter" aria-label="Project" className="h-8 min-w-40">
            <SelectValue>{projects.find((item) => item.id === project)?.name ?? "All projects"}</SelectValue>
          </SelectTrigger>
          <SelectContent align="start">
            <SelectItem value="all">All projects</SelectItem>
            {projects.map((item) => <SelectItem key={item.id} value={item.id}>{item.name}</SelectItem>)}
          </SelectContent>
        </Select>
      </div>
      <Button
        type="button"
        variant="outline"
        size="sm"
        aria-pressed={includeArchived}
        onClick={() => { update("state", includeArchived ? "" : "all"); }}
      >
        {includeArchived ? "Hide archived" : "Show archived"}
      </Button>
    </div>
  );
}
