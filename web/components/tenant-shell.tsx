"use client";

import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import type { ReactNode } from "react";
import { ChevronDown, Gauge, HardDrive, Layers3 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { LogoutForm } from "@/components/logout-form";
import { cn } from "@/lib/utils";

export type TenantOption = Readonly<{ slug: string; name: string }>;

export function TenantShell({
  tenant,
  tenants,
  userLabel,
  csrfToken,
  logoutUrl,
  children,
}: {
  tenant: TenantOption;
  tenants: TenantOption[];
  userLabel: string;
  csrfToken: string;
  logoutUrl: string;
  children: ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const navigation = [
    { href: `/t/${tenant.slug}`, label: "Dashboard", icon: Gauge, active: pathname === `/t/${tenant.slug}` },
    { href: `/t/${tenant.slug}/jobs`, label: "Jobs", icon: HardDrive, active: pathname.startsWith(`/t/${tenant.slug}/jobs`) },
  ];
  const future = ["Workflows", "Executions", "Clusters", "Usage", "Admin"];

  return (
    <div className="min-h-screen">
      <header className="sticky top-0 z-20 flex h-16 items-center justify-between border-b border-slate-200 bg-white/95 px-5 backdrop-blur">
        <Link href={`/t/${tenant.slug}`} className="text-lg font-bold tracking-tight text-teal-800">Custos</Link>
        <div className="flex items-center gap-3">
          <label className="sr-only" htmlFor="tenant-switcher">Tenant</label>
          <select
            id="tenant-switcher"
            aria-label="Switch tenant"
            className="h-9 max-w-48 rounded-md border border-slate-300 bg-white px-3 text-sm"
            value={tenant.slug}
            onChange={(event) => { router.push(`/t/${encodeURIComponent(event.target.value)}`); }}
          >
            {tenants.map((item) => <option key={item.slug} value={item.slug}>{item.name}</option>)}
          </select>
          <DropdownMenu.Root>
            <DropdownMenu.Trigger asChild>
              <Button variant="ghost" size="sm" aria-label="User menu">
                {userLabel}<ChevronDown aria-hidden="true" className="h-4 w-4" />
              </Button>
            </DropdownMenu.Trigger>
            <DropdownMenu.Portal>
              <DropdownMenu.Content align="end" className="z-50 min-w-48 rounded-lg border border-slate-200 bg-white p-2 shadow-lg">
                <DropdownMenu.Label className="px-3 py-2 text-xs text-slate-500">{userLabel}</DropdownMenu.Label>
                <DropdownMenu.Separator className="my-1 h-px bg-slate-100" />
                <DropdownMenu.Item asChild onSelect={(event) => { event.preventDefault(); }}>
                  <div className="rounded-md outline-none focus:bg-slate-100">
                    <LogoutForm csrfToken={csrfToken} logoutUrl={logoutUrl} />
                  </div>
                </DropdownMenu.Item>
              </DropdownMenu.Content>
            </DropdownMenu.Portal>
          </DropdownMenu.Root>
        </div>
      </header>
      <div className="mx-auto grid min-h-[calc(100vh-4rem)] max-w-[1600px] grid-cols-1 md:grid-cols-[220px_minmax(0,1fr)]">
        <aside className="border-b border-slate-200 bg-white p-4 md:border-b-0 md:border-r">
          <p className="mb-3 px-3 text-xs font-semibold uppercase tracking-wider text-slate-500">Workspace</p>
          <nav aria-label="Main navigation" className="space-y-1">
            {navigation.map(({ href, label, icon: Icon, active }) => (
              <Link
                aria-current={active ? "page" : undefined}
                className={cn("flex items-center gap-3 rounded-md px-3 py-2.5 text-sm", active ? "bg-teal-50 font-semibold text-teal-800" : "text-slate-700 hover:bg-slate-100")}
                href={href}
                key={href}
              >
                <Icon aria-hidden="true" className="h-4 w-4" />
                {label}
              </Link>
            ))}
            <div className="my-3 border-t border-slate-100" />
            {future.map((label) => (
              <div aria-disabled="true" className="flex items-center gap-3 rounded-md px-3 py-2.5 text-sm text-slate-400" key={label} title="Coming soon">
                <Layers3 aria-hidden="true" className="h-4 w-4" />
                <span>{label}</span>
                <span className="ml-auto text-[10px] uppercase tracking-wide">Soon</span>
              </div>
            ))}
          </nav>
        </aside>
        <main className="min-w-0 p-5 md:p-8">{children}</main>
      </div>
    </div>
  );
}
