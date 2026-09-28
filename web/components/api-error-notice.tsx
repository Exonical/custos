import { ApiError } from "@/lib/api/client";
import { Card, CardContent } from "@/components/ui/card";

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
    <Card role="alert" className="max-w-2xl border-rose-200">
      <CardContent className="space-y-2">
        <h2 className="font-semibold text-slate-900">{message}</h2>
        <p className="font-mono text-xs text-slate-600">{code}</p>
        {requestId ? <p className="text-xs text-slate-500">Request ID: {requestId}</p> : null}
      </CardContent>
    </Card>
  );
}
