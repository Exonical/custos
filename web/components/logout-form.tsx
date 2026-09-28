"use client";

import { useState, type SyntheticEvent } from "react";
import { Button } from "@/components/ui/button";

export function LogoutForm({ csrfToken, logoutUrl }: { csrfToken: string; logoutUrl: string }) {
  const [error, setError] = useState(false);

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(false);
    try {
      const response = await fetch("/auth/logout", {
        method: "POST",
        credentials: "same-origin",
        cache: "no-store",
        redirect: "manual",
        headers: { "X-CSRF-Token": csrfToken },
      });
      if (response.status !== 303 && response.type !== "opaqueredirect") throw new Error("logout rejected");
      window.location.assign(logoutUrl);
    } catch {
      setError(true);
    }
  }

  return (
    <form action="/auth/logout" method="post" className="w-full" onSubmit={(event) => { void submit(event); }}>
      <Button type="submit" variant="ghost" className="h-8 w-full justify-start rounded-none px-2 font-mono text-xs uppercase tracking-[0.06em]">
        Sign out
      </Button>
      {error ? <p role="alert" className="px-2 py-2 font-mono text-[10px] uppercase text-destructive">Sign-out failed. Reload and retry.</p> : null}
    </form>
  );
}
