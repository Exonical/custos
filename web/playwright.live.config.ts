import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.CUSTOS_WEB_PUBLIC_ORIGIN ?? "https://127.0.0.1:3000";

export default defineConfig({
  testDir: "./e2e",
  testMatch: "live.spec.ts",
  fullyParallel: false,
  retries: 0,
  reporter: "list",
  use: {
    ...devices["Desktop Chrome"],
    baseURL,
    ignoreHTTPSErrors: true,
    launchOptions: {
      args: ["--host-resolver-rules=MAP keycloak.e2e 127.0.0.1"],
    },
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
});
