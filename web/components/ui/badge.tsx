import { mergeProps } from "@base-ui/react/merge-props"
import { useRender } from "@base-ui/react/use-render"
import { cva, type VariantProps } from "class-variance-authority"
import { cn } from "@/lib/utils"

const badgeVariants = cva(
  "group/badge inline-flex h-5 w-fit shrink-0 items-center justify-center gap-1.5 overflow-hidden rounded-none border border-transparent px-2 py-0.5 font-mono text-[10px] font-medium uppercase tracking-[0.08em] whitespace-nowrap transition-colors focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 has-data-[icon=inline-end]:pr-1.5 has-data-[icon=inline-start]:pl-1.5 aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:border-destructive/40 [&>svg]:pointer-events-none [&>svg]:size-3!",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground [a]:hover:bg-primary/80",
        secondary:
          "bg-secondary text-secondary-foreground [a]:hover:bg-secondary/80",
        destructive:
          "bg-destructive/10 text-destructive focus-visible:ring-destructive/20 dark:bg-destructive/20 dark:focus-visible:ring-destructive/40 [a]:hover:bg-destructive/20",
        outline:
          "border-border text-foreground [a]:hover:bg-muted [a]:hover:text-muted-foreground",
        ghost:
          "hover:bg-muted hover:text-muted-foreground dark:hover:bg-muted/50",
        link: "text-primary underline-offset-4 hover:underline",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
)

const stateStyles: Record<string, string> = {
  RUNNING: "border-status-running/35 bg-status-running/10 text-status-running",
  SUCCEEDED: "border-status-completed/35 bg-status-completed/10 text-status-completed",
  COMPLETED: "border-status-completed/35 bg-status-completed/10 text-status-completed",
  QUEUED: "border-status-queued/35 bg-status-queued/10 text-status-queued",
  SUBMITTING: "border-status-queued/35 bg-status-queued/10 text-status-queued",
  PENDING: "border-status-queued/35 bg-status-queued/10 text-status-queued",
  VALIDATING: "border-status-queued/35 bg-status-queued/10 text-status-queued",
  READY: "border-status-queued/35 bg-status-queued/10 text-status-queued",
  BLOCKED: "border-status-degraded/35 bg-status-degraded/10 text-status-degraded",
  ADMITTING: "border-status-degraded/35 bg-status-degraded/10 text-status-degraded",
  CANCELING: "border-status-canceled/35 bg-status-canceled/10 text-status-canceled",
  CANCELED: "border-status-canceled/35 bg-status-canceled/10 text-status-canceled",
  SKIPPED: "border-status-completed/35 bg-status-completed/10 text-status-completed",
  FAILED: "border-status-failed/35 bg-status-failed/10 text-status-failed",
  PARTIAL_FAILURE: "border-status-degraded/35 bg-status-degraded/10 text-status-degraded",
  active: "border-status-active/35 bg-status-active/10 text-status-active",
  archived: "border-status-canceled/35 bg-status-canceled/10 text-status-canceled",
  draft: "border-status-queued/35 bg-status-queued/10 text-status-queued",
  published: "border-status-active/35 bg-status-active/10 text-status-active",
  deprecated: "border-status-canceled/35 bg-status-canceled/10 text-status-canceled",
};

function Badge({
  className,
  variant = "default",
  render,
  ...props
}: useRender.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return useRender({
    defaultTagName: "span",
    props: mergeProps<"span">(
      {
        className: cn(badgeVariants({ variant }), className),
      },
      props
    ),
    render,
    state: {
      slot: "badge",
      variant,
    },
  })
}

function StateBadge({ state }: { state: string }) {
  return (
    <Badge variant="outline" className={cn("rounded-none", stateStyles[state] ?? "border-border bg-muted text-muted-foreground")}>
      <span aria-hidden="true" className="text-[0.65em] leading-none">■</span>
      {state}
    </Badge>
  );
}

export { Badge, StateBadge, badgeVariants }
