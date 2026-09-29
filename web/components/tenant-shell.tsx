"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { createContext, useContext, useEffect, useState, useSyncExternalStore, type ReactNode } from "react";
import { ChartNoAxesColumn, ChevronDown, ChevronsUpDown, FolderKanban, Gauge, HardDrive, KeyRound, Layers3, Menu, Server } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { LogoutForm } from "@/components/logout-form";
import { Breadcrumb, BreadcrumbItem, BreadcrumbLink, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from "@/components/ui/breadcrumb";
import { Separator } from "@/components/ui/separator";
import { cn } from "@/lib/utils";

export type TenantOption = Readonly<{ slug: string; name: string }>;

const TenantContext = createContext<TenantOption | null>(null);

export function TenantDisplayName({ fallback }: { fallback: string }) {
  const tenant = useContext(TenantContext);
  return <>{tenant?.name || fallback}</>;
}

const routeSections = {
  jobs: { label: "Jobs", icon: HardDrive },
  projects: { label: "Projects", icon: FolderKanban },
  clusters: { label: "Clusters", icon: Server },
  secrets: { label: "Secrets", icon: KeyRound },
  usage: { label: "Usage", icon: ChartNoAxesColumn },
} as const;

type WorkspaceLink = Readonly<{ label: string; href?: string; icon: typeof Layers3 }>;
type WorkspaceSection = Readonly<{ label: string; items: readonly WorkspaceLink[] }>;
type BreadcrumbEntityStore = {
  getServerSnapshot: () => null;
  getSnapshot: () => string | null;
  set: (label: string | null) => void;
  subscribe: (listener: () => void) => () => void;
};

function createBreadcrumbEntityStore(): BreadcrumbEntityStore {
  let entity: string | null = null;
  const listeners = new Set<() => void>();
  return {
    getServerSnapshot: () => null,
    getSnapshot: () => entity,
    set: (label) => {
      if (entity === label) return;
      entity = label;
      listeners.forEach((listener) => { listener(); });
    },
    subscribe: (listener) => {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
  };
}

const BreadcrumbEntityContext = createContext<BreadcrumbEntityStore | null>(null);

export function BreadcrumbEntity({ label }: { label: string }) {
  const store = useContext(BreadcrumbEntityContext);
  useEffect(() => {
    if (!store) return;
    store.set(label);
    return () => { store.set(null); };
  }, [label, store]);
  return null;
}

function TenantSwitcher({
  tenant,
  tenants,
  onChange,
}: {
  tenant: TenantOption;
  tenants: TenantOption[];
  onChange: (slug: string) => void;
}) {
  return (
    <div className="grid gap-2 px-3 py-4">
      <p className="font-mono text-[9px] uppercase tracking-[0.14em] text-muted-foreground">TENANT</p>
      <DropdownMenu>
        <DropdownMenuTrigger render={<Button variant="outline" size="sm" aria-label="Switch tenant" title={tenant.name} className="h-auto w-full min-w-0 justify-between gap-2 px-2.5 py-2 text-left" />}>
          <span className="grid min-w-0 flex-1 gap-1 text-left">
            <span className="truncate font-sans text-sm normal-case tracking-[0.02em]">{tenant.name}</span>
            <span className="truncate font-mono text-[9px] text-muted-foreground">{tenant.slug}</span>
          </span>
          <ChevronsUpDown aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="min-w-56 border-border bg-popover shadow-none">
          <DropdownMenuGroup>
            <DropdownMenuLabel className="font-mono text-[9px] uppercase tracking-[0.12em] text-muted-foreground">TENANTS</DropdownMenuLabel>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuRadioGroup value={tenant.slug} onValueChange={(value) => { if (typeof value === "string") onChange(value); }}>
            {tenants.map((item) => (
              <DropdownMenuRadioItem key={item.slug} value={item.slug} className="min-w-0 font-sans text-sm normal-case">
                <span className="truncate">{item.name}</span>
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

function WorkspaceNavigation({
  tenant,
  pathname,
  onNavigate,
}: {
  tenant: TenantOption;
  pathname: string;
  onNavigate?: () => void;
}) {
  const sections: WorkspaceSection[] = [
    {
      label: "WORKLOADS",
      items: [
        { label: "Jobs", href: `/t/${tenant.slug}/jobs`, icon: HardDrive },
        { label: "Workflows", icon: Layers3 },
        { label: "Templates", icon: Layers3 },
        { label: "Interactive", icon: Layers3 },
      ],
    },
    {
      label: "RESOURCES",
      items: [
        { label: "Projects", href: `/t/${tenant.slug}/projects`, icon: FolderKanban },
        { label: "Secrets", href: `/t/${tenant.slug}/secrets`, icon: KeyRound },
        { label: "Usage", href: `/t/${tenant.slug}/usage`, icon: ChartNoAxesColumn },
      ],
    },
    {
      label: "INFRASTRUCTURE",
      items: [
        { label: "Clusters", href: `/t/${tenant.slug}/clusters`, icon: Server },
        { label: "Policies", icon: Layers3 },
      ],
    },
  ];
  const dashboardHref = `/t/${tenant.slug}`;

  function navClass(active: boolean) {
    return cn(
      "relative flex min-w-0 items-center gap-3 px-3 py-2 font-mono text-[10px] uppercase tracking-[0.08em] transition-colors",
      active
        ? "bg-sidebar-accent text-primary before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-primary"
        : "text-muted-foreground hover:bg-muted hover:text-foreground",
    );
  }

  return (
    <nav aria-label="Main navigation" className="space-y-5">
      <Link
        aria-current={pathname === dashboardHref ? "page" : undefined}
        className={navClass(pathname === dashboardHref)}
        href={dashboardHref}
        onClick={onNavigate}
      >
        <Gauge aria-hidden="true" className="size-4 shrink-0" />
        <span>Dashboard</span>
      </Link>
      {sections.map((section) => (
        <section key={section.label} className="space-y-1">
          <h2 className="px-3 pb-1 font-mono text-[9px] font-medium uppercase tracking-[0.14em] text-muted-foreground">{section.label}</h2>
          {section.items.map((item) => {
            const active = item.href ? pathname.startsWith(item.href) : false;
            const Icon = item.icon;
            return item.href ? (
              <Link
                key={item.label}
                aria-current={active ? "page" : undefined}
                className={navClass(active)}
                href={item.href}
                onClick={onNavigate}
              >
                <Icon aria-hidden="true" className="size-4 shrink-0" />
                <span>{item.label}</span>
              </Link>
            ) : (
              <div aria-disabled="true" key={item.label} className="flex min-w-0 items-center gap-3 px-3 py-2 font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground">
                <Icon aria-hidden="true" className="size-4 shrink-0 opacity-60" />
                <span className="truncate">{item.label}</span>
                <span className="ml-auto border border-border px-1 py-0.5 text-[8px] leading-none tracking-[0.1em]">SOON</span>
              </div>
            );
          })}
        </section>
      ))}
    </nav>
  );
}

function TenantBrand({ tenantSlug }: { tenantSlug: string }) {
  return (
    <Link href={`/t/${tenantSlug}`} className="grid gap-1">
      <span className="font-mono text-base font-semibold tracking-[0.18em]">CUSTOS</span>
      <span className="font-mono text-[9px] uppercase tracking-[0.14em] text-muted-foreground">HPC CONTROL PLANE</span>
    </Link>
  );
}

export function TenantShell({
  tenant,
  tenants,
  userLabel,
  userEmail,
  csrfToken,
  logoutUrl,
  children,
}: {
  tenant: TenantOption;
  tenants: TenantOption[];
  userLabel: string;
  userEmail: string;
  csrfToken: string;
  logoutUrl: string;
  children: ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [breadcrumbStore] = useState(createBreadcrumbEntityStore);
  const breadcrumbEntity = useSyncExternalStore(breadcrumbStore.subscribe, breadcrumbStore.getSnapshot, breadcrumbStore.getServerSnapshot);
  const routeSection = pathname.split("/").filter(Boolean)[2];
  const section = routeSection && Object.hasOwn(routeSections, routeSection)
    ? routeSections[routeSection as keyof typeof routeSections]
    : undefined;
  const sectionLabel = section ? section.label : "Dashboard";
  const sectionHref = section && routeSection ? `/t/${tenant.slug}/${routeSection}` : `/t/${tenant.slug}`;
  const SectionIcon = section ? section.icon : Gauge;
  const initials = userLabel.split(/\s+/).map((part) => part[0]).filter(Boolean).slice(0, 2).join("").toUpperCase();
  const selectTenant = (slug: string) => { router.push(`/t/${encodeURIComponent(slug)}`); };

  return (
    <TenantContext.Provider value={tenant}>
      <BreadcrumbEntityContext.Provider value={breadcrumbStore}>
      <div className="min-h-screen">
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-60 flex-col border-r border-border bg-sidebar md:flex">
        <div className="border-b border-border px-4 py-4"><TenantBrand tenantSlug={tenant.slug} /></div>
        <TenantSwitcher tenant={tenant} tenants={tenants} onChange={selectTenant} />
        <Separator />
        <div className="min-h-0 flex-1 overflow-y-auto px-3 py-4">
          <WorkspaceNavigation tenant={tenant} pathname={pathname} />
        </div>
      </aside>

      <div className="min-h-screen min-w-0 md:pl-60">
        <header className="sticky top-0 z-20 flex h-14 min-w-0 items-center justify-between gap-3 border-b border-border bg-background/95 px-3 backdrop-blur md:px-6">
          <div className="flex min-w-0 items-center gap-3">
            <Dialog open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
              <DialogTrigger render={<Button variant="ghost" size="icon-sm" aria-label="Open navigation menu" className="md:hidden" />}>
                <Menu aria-hidden="true" className="size-4" />
              </DialogTrigger>
              <DialogContent
                showCloseButton={false}
                className="fixed inset-y-0 left-0 top-0 z-50 flex h-dvh w-[min(84vw,15rem)] max-w-none translate-x-0 translate-y-0 flex-col gap-0 rounded-none border-r border-border bg-sidebar p-0 text-foreground shadow-none outline-none sm:max-w-none md:hidden"
              >
                <DialogTitle className="sr-only">Navigation</DialogTitle>
                <div className="border-b border-border px-4 py-4"><TenantBrand tenantSlug={tenant.slug} /></div>
                <TenantSwitcher tenant={tenant} tenants={tenants} onChange={selectTenant} />
                <Separator />
                <div className="min-h-0 flex-1 overflow-y-auto p-3">
                  <WorkspaceNavigation tenant={tenant} pathname={pathname} onNavigate={() => { setMobileNavOpen(false); }} />
                </div>
              </DialogContent>
            </Dialog>
            <Breadcrumb className="min-w-0">
              <BreadcrumbList className="min-w-0 flex-nowrap font-mono text-[10px] tracking-[0.08em]">
                <BreadcrumbItem className="shrink-0">
                  <BreadcrumbLink className="inline-flex items-center gap-2" render={<Link href={sectionHref} />}>
                    <SectionIcon aria-hidden="true" className="size-3.5 shrink-0" />
                    <span className="uppercase">{sectionLabel}</span>
                  </BreadcrumbLink>
                </BreadcrumbItem>
                {breadcrumbEntity ? (
                  <>
                    <BreadcrumbSeparator />
                    <BreadcrumbItem className="min-w-0">
                      <BreadcrumbPage title={breadcrumbEntity} className="block max-w-[min(36vw,14rem)] truncate normal-case">{breadcrumbEntity}</BreadcrumbPage>
                    </BreadcrumbItem>
                  </>
                ) : null}
              </BreadcrumbList>
            </Breadcrumb>
          </div>

          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="ghost" size="sm" aria-label="User menu" className="h-8 shrink-0 gap-2 px-1.5 sm:px-2" />}>
              <span aria-hidden="true" className="grid size-8 shrink-0 place-items-center border border-primary/40 bg-primary/10 font-mono text-[10px] font-semibold text-primary">{initials}</span>
              <span className="hidden min-w-0 text-left leading-tight sm:grid">
                <span className="max-w-40 truncate font-sans text-xs font-medium normal-case text-foreground">{userLabel}</span>
                <span className="max-w-40 truncate font-mono text-[9px] normal-case text-muted-foreground">{userEmail || "Email unavailable"}</span>
              </span>
              <ChevronDown aria-hidden="true" className="hidden size-3.5 shrink-0 text-muted-foreground sm:block" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-56 border-border bg-popover shadow-none">
              <DropdownMenuGroup>
                <DropdownMenuLabel className="grid gap-1 font-sans normal-case">
                  <span className="truncate text-xs font-medium text-foreground">{userLabel}</span>
                  <span className="truncate font-mono text-[9px] text-muted-foreground">{userEmail || "Email unavailable"}</span>
                </DropdownMenuLabel>
              </DropdownMenuGroup>
              <DropdownMenuSeparator />
              <DropdownMenuItem closeOnClick={false} className="p-0 focus:bg-transparent">
                <LogoutForm csrfToken={csrfToken} logoutUrl={logoutUrl} />
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </header>
        <main className="min-w-0 p-4 md:p-6">{children}</main>
      </div>
      </div>
      </BreadcrumbEntityContext.Provider>
    </TenantContext.Provider>
  );
}
