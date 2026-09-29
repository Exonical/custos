"use client";

import { useMemo, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import type { AllocationList, ClusterBindingList, ProjectMembershipList, TenantClusterList } from "@/lib/api/client";
import { AllocationBudgetBar } from "@/components/allocation-budget-bar";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatHours, formatUtcDateTime } from "@/lib/format";

const tabValues = ["members", "bindings", "allocations"] as const;
type ProjectTab = (typeof tabValues)[number];

export function ProjectDetailTabs({
  members,
  bindings,
  clusters,
  allocations,
  selectedTab,
}: {
  members: ProjectMembershipList["items"];
  bindings: ClusterBindingList["items"];
  clusters: TenantClusterList["items"];
  allocations: AllocationList["items"];
  selectedTab: ProjectTab;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const [activeTab, setActiveTab] = useState<ProjectTab>(selectedTab);
  const clusterById = useMemo(() => new Map(clusters.map((cluster) => [cluster.id, cluster])), [clusters]);

  function changeTab(value: string | null) {
    if (!value || !tabValues.includes(value as ProjectTab)) return;
    setActiveTab(value as ProjectTab);
    const params = new URLSearchParams(window.location.search);
    if (value === "members") params.delete("tab");
    else params.set("tab", value);
    router.replace(`${pathname}${params.size ? `?${params.toString()}` : ""}`, { scroll: false });
  }

  return (
    <Tabs value={activeTab} onValueChange={changeTab}>
      <TabsList variant="line" className="h-8 w-full justify-start border-b border-border px-3">
        <TabsTrigger value="members">Members</TabsTrigger>
        <TabsTrigger value="bindings">Cluster bindings</TabsTrigger>
        <TabsTrigger value="allocations">Allocations</TabsTrigger>
      </TabsList>
      <TabsContent value="members" className="min-w-0">
        <div className="overflow-hidden bg-card">
          <Table containerClassName="max-h-[60vh]">
            <caption className="sr-only">Members of this project</caption>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>User</TableHead>
                <TableHead>Roles</TableHead>
                <TableHead>Source</TableHead>
                <TableHead>Added</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {members.length === 0 ? (
                <TableRow><TableCell colSpan={4} className="py-10 text-center">
                  <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No members</p>
                  <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
                </TableCell></TableRow>
              ) : members.map((member) => (
                <TableRow key={member.user_id}>
                  <TableCell className="font-mono text-xs">{member.user_id}</TableCell>
                  <TableCell className="font-mono text-xs">{member.roles.join(", ")}</TableCell>
                  <TableCell className="font-mono text-xs">{member.source}</TableCell>
                  <TableCell className="font-mono text-xs tabular-nums">{formatUtcDateTime(member.created_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </TabsContent>
      <TabsContent value="bindings" className="min-w-0">
        <div className="overflow-hidden bg-card">
          <Table containerClassName="max-h-[60vh]">
            <caption className="sr-only">Cluster bindings for this project</caption>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Cluster</TableHead>
                <TableHead>Slurm account</TableHead>
                <TableHead>Partitions</TableHead>
                <TableHead>QoS</TableHead>
                <TableHead>Drift</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {bindings.length === 0 ? (
                <TableRow><TableCell colSpan={5} className="py-10 text-center">
                  <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No cluster bindings</p>
                  <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
                </TableCell></TableRow>
              ) : bindings.map((binding) => {
                const cluster = clusterById.get(binding.cluster_id);
                const clusterName = cluster?.display_name || cluster?.name || binding.cluster_id;
                const partitions = binding.allowed_partitions.length > 0 ? binding.allowed_partitions : [binding.default_partition];
                const qos = binding.allowed_qos.length > 0 ? binding.allowed_qos : [binding.default_qos];
                return (
                  <TableRow key={binding.id}>
                    <TableCell title={binding.cluster_id}>{clusterName}</TableCell>
                    <TableCell className="font-mono text-xs">{binding.slurm_account}</TableCell>
                    <TableCell className="font-mono text-xs">{partitions.join(", ")}</TableCell>
                    <TableCell className="font-mono text-xs">{qos.join(", ")}</TableCell>
                    <TableCell className="font-mono text-xs uppercase">{binding.drift_state ?? "—"}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      </TabsContent>
      <TabsContent value="allocations" className="min-w-0">
        <div className="overflow-hidden bg-card">
          <Table containerClassName="max-h-[60vh]">
            <caption className="sr-only">Project allocations</caption>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Resource</TableHead>
                <TableHead>Budget</TableHead>
                <TableHead>Consumed</TableHead>
                <TableHead>Enforcement</TableHead>
                <TableHead>Period</TableHead>
                <TableHead>Usage</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {allocations.length === 0 ? (
                <TableRow><TableCell colSpan={6} className="py-10 text-center">
                  <p className="font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No allocations</p>
                  <p className="mt-1 text-xs text-muted-foreground">No data available.</p>
                </TableCell></TableRow>
              ) : allocations.map((allocation) => (
                <TableRow key={allocation.id}>
                  <TableCell><span className="font-mono text-xs uppercase">{allocation.unit}</span><span className="ml-2 text-xs text-muted-foreground">{allocation.name}</span></TableCell>
                  <TableCell className="font-mono text-xs tabular-nums">{formatHours(allocation.limit_amount)}</TableCell>
                  <TableCell className="font-mono text-xs tabular-nums">{formatHours(allocation.consumed_amount)}</TableCell>
                  <TableCell className="font-mono text-xs uppercase">{allocation.enforcement}</TableCell>
                  <TableCell className="font-mono text-[10px] tabular-nums">{formatUtcDateTime(allocation.period_start)} – {formatUtcDateTime(allocation.period_end)}</TableCell>
                  <TableCell><AllocationBudgetBar consumed={allocation.consumed_amount} budget={allocation.limit_amount} /></TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </TabsContent>
    </Tabs>
  );
}
