export async function register(): Promise<void> {
  if (process.env.NEXT_PHASE === "phase-production-build") return;
  if (process.env.NEXT_RUNTIME !== "nodejs") return;

  const [{ getConfig }, { ZodError }] = await Promise.all([
    import("@/lib/config"),
    import("zod"),
  ]);
  try {
    const config = getConfig();
    process.env.AUTH_URL = config.publicOrigin;
  } catch (error) {
    if (error instanceof ZodError) {
      const variables = [...new Set(error.issues.map((issue) => String(issue.path[0])).filter((name) => name !== "undefined"))];
      throw new Error(`Custos web startup requires valid environment variables: ${variables.join(", ")}. See web/.env.example or run pnpm dev:mock for a backend-free UI.`);
    }
    throw error;
  }
}
