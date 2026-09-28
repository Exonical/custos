import { defineConfig, devices } from "@playwright/test";

const externalMock = process.env.CUSTOS_WEB_MOCK_EXTERNAL === "1";
const baseURL = process.env.CUSTOS_WEB_PUBLIC_ORIGIN ?? "http://localhost:3005";

export default defineConfig({
  testDir: "./e2e",
  testMatch: "smoke.spec.ts",
  fullyParallel: false,
  retries: 0,
  reporter: "list",
  use: {
    ...devices["Desktop Chrome"],
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: externalMock ? [] : [
    {
      command: "pnpm dev:mock --hostname localhost --port 3005",
      url: "http://localhost:3005/api/health",
      reuseExistingServer: false,
      timeout: 120_000,
      env: {
        MOCK_OIDC_AUTO_LOGIN: process.env.MOCK_OIDC_AUTO_LOGIN ?? "alice",
        MOCK_OIDC_TOKEN_TTL: process.env.MOCK_OIDC_TOKEN_TTL ?? "120",
        MOCK_API_LATENCY_MS: process.env.MOCK_API_LATENCY_MS ?? "0",
      },
    },
  ],
});
