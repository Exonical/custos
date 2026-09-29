import Link from "next/link";
import { redirect } from "next/navigation";
import { ApiErrorNotice } from "@/components/api-error-notice";
import { TemplateTags } from "@/components/workflows/template-tags";
import { createApiClient, toApiError } from "@/lib/api/client";
import { requireServerSession } from "@/lib/session/server";

export default async function TemplatesPage({ params }: { params: Promise<{ tenant: string }> }) {
  const { tenant } = await params;
  const returnTo = `/t/${encodeURIComponent(tenant)}/templates`;
  const session = await requireServerSession(returnTo);
  const response = await createApiClient(session.accessToken).GET("/workflow-templates");
  if (response.error) {
    if (response.response.status === 401) redirect(`/auth/login?returnTo=${encodeURIComponent(returnTo)}`);
    return <ApiErrorNotice error={toApiError(response.error, response.response.status)} />;
  }
  const templates = response.data.items;
  return (
    <section className="space-y-6">
      <header className="border-b border-border pb-4">
        <h1 className="text-xl font-semibold">Templates</h1>
        <p className="mt-1 max-w-3xl text-sm text-muted-foreground">
          Starting points for new workflows. Account, partition and QoS come from your project&apos;s cluster binding; using a template creates a draft workflow you can edit before publishing.
        </p>
      </header>
      {templates.length === 0 ? (
        <p className="border border-border bg-card px-4 py-10 text-center font-mono text-xs uppercase tracking-[0.1em] text-muted-foreground">No templates</p>
      ) : (
        <ul aria-label="Workflow templates" className="grid gap-px border border-border bg-border sm:grid-cols-2 xl:grid-cols-4">
          {templates.map((template) => (
            <li key={template.id} className="bg-card">
              <Link href={`/t/${encodeURIComponent(tenant)}/templates/${encodeURIComponent(template.id)}`} className="flex h-full flex-col gap-2 p-4 hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                <span className="font-medium text-foreground">{template.title}</span>
                <span className="text-xs text-muted-foreground">{template.summary}</span>
                <span className="mt-auto flex flex-col gap-2 pt-2">
                  <TemplateTags tags={template.tags} />
                  <span className="font-mono text-[9px] uppercase tracking-[0.1em] text-primary">{template.id}</span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
