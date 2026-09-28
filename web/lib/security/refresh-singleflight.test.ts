import { describe, expect, it } from "vitest";
import { refreshSingleFlight, resetRefreshFlightsForTest } from "@/lib/security/refresh-singleflight";

describe("refresh single-flight", () => {
  it("shares one token endpoint call for concurrent refreshes of the same token", async () => {
    resetRefreshFlightsForTest();
    let calls = 0;
    let finish!: (value: string) => void;
    const operation = () => {
      calls += 1;
      return new Promise<string>((resolve) => { finish = resolve; });
    };
    const first = refreshSingleFlight("refresh-token", operation);
    const second = refreshSingleFlight("refresh-token", operation);
    await Promise.resolve();
    expect(calls).toBe(1);
    finish("rotated-refresh-token");
    await expect(Promise.all([first, second])).resolves.toEqual([
      "rotated-refresh-token",
      "rotated-refresh-token",
    ]);
  });

  it("does not share refreshes for different token hashes", async () => {
    resetRefreshFlightsForTest();
    let calls = 0;
    const operation = () => Promise.resolve(`result-${String(++calls)}`);
    await Promise.all([
      refreshSingleFlight("token-a", operation),
      refreshSingleFlight("token-b", operation),
    ]);
    expect(calls).toBe(2);
  });
});
