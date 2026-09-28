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
    <form action="/auth/logout" method="post" onSubmit={(event) => { void submit(event); }}>
      <Button type="submit" variant="ghost" className="w-full justify-start">
        Sign out
      </Button>
      {error ? <p role="alert" className="px-3 py-2 text-xs text-rose-700">Sign-out could not be completed. Reload and try again.</p> : null}
    </form>
  );
}
