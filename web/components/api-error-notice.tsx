import { ApiError } from "@/lib/api/client";
import { Card, CardContent, CardHeader } from "@/components/ui/card";

export function ApiErrorNotice({ error }: { error: unknown }) {
  const status = error instanceof ApiError ? error.status : 500;
  const code = error instanceof ApiError ? error.code : "INTERNAL_ERROR";
  const requestId = error instanceof ApiError ? error.requestId : null;
  const message = status === 403
    ? "You do not have permission to view this information."
    : status === 404
      ? "This item could not be found."
      : "Custos could not load this information. Try again later.";

  return (
    <Card role="alert" className="max-w-2xl border-destructive/35">
      <CardHeader className="border-b border-border pb-3">
        <p className="font-mono text-[10px] uppercase tracking-[0.12em] text-destructive">{`// API error ${String(status)}`}</p>
      </CardHeader>
      <CardContent className="space-y-2 pt-4">
        <h2 className="text-sm font-semibold">{message}</h2>
        <p className="font-mono text-xs uppercase tracking-[0.08em] text-muted-foreground">{code}</p>
        {requestId ? <p className="font-mono text-[10px] text-muted-foreground">Request ID: {requestId}</p> : null}
      </CardContent>
    </Card>
  );
}
