import { defineConfig, devices } from "@playwright/test";

const externalMock = process.env.CUSTOS_WEB_MOCK_EXTERNAL === "1";
const webPort = process.env.PORT ?? "3005";
const baseURL = process.env.CUSTOS_WEB_PUBLIC_ORIGIN ?? `http://localhost:${webPort}`;

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
      command: `pnpm dev:mock --hostname localhost --port ${webPort}`,
      url: `${baseURL}/api/health`,
      reuseExistingServer: false,
      timeout: 120_000,
      env: {
        PORT: webPort,
        MOCK_OIDC_PORT: process.env.MOCK_OIDC_PORT ?? "4300",
        MOCK_API_PORT: process.env.MOCK_API_PORT ?? "4301",
        MOCK_OIDC_AUTO_LOGIN: process.env.MOCK_OIDC_AUTO_LOGIN ?? process.env.MOCK_USER ?? "alice",
        MOCK_OIDC_TOKEN_TTL: process.env.MOCK_OIDC_TOKEN_TTL ?? "120",
        MOCK_API_LATENCY_MS: process.env.MOCK_API_LATENCY_MS ?? "0",
      },
    },
  ],
});
