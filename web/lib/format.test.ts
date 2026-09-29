import { describe, expect, it } from "vitest";
import { formatDurationBetween, formatDurationSeconds, formatHours, formatSecondsAsHours, formatUtcDate, formatUtcDateTime } from "@/lib/format";

describe("formatUtcDateTime", () => {
  it("formats timestamps in UTC", () => {
    expect(formatUtcDateTime("2026-09-28T12:34:00.000Z")).toBe("Sep 28, 2026, 12:34 PM");
  });

  it("uses an em dash for missing or invalid timestamps", () => {
    expect(formatUtcDate(null)).toBe("—");
    expect(formatUtcDateTime("invalid")).toBe("—");
  });
});

describe("duration formatting", () => {
  it("formats seconds and elapsed timestamp pairs", () => {
    expect(formatDurationSeconds(3_661)).toBe("1h 1m");
    expect(formatDurationBetween("2026-09-28T12:00:00Z", "2026-09-28T13:01:02Z")).toBe("1h 1m");
    expect(formatDurationSeconds(null)).toBe("—");
    expect(formatDurationBetween("2026-09-28T13:00:00Z", "2026-09-28T12:00:00Z")).toBe("—");
  });

  it("formats hour values consistently", () => {
    expect(formatHours(1.256)).toBe("1.26 h");
    expect(formatHours(Number.NaN)).toBe("—");
  });

  it("converts accounting seconds to hours for presentation", () => {
    expect(formatSecondsAsHours(3_600)).toBe("1 h");
    expect(formatSecondsAsHours(5_400)).toBe("1.5 h");
    expect(formatSecondsAsHours(null)).toBe("—");
  });
});
