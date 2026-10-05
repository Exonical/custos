import { describe, expect, it } from "vitest";
import { imagePullReferenceNames } from "./image-pull-secret-editor";

describe("imagePullReferenceNames", () => {
  it("returns names of references allowed for image_pull", () => {
    expect(imagePullReferenceNames({
      items: [
        { name: "registry", allowed_uses: ["image_pull"] },
        { name: "env-only", allowed_uses: ["env"] },
        { name: "both", allowed_uses: ["env", "image_pull"] },
      ],
    })).toEqual(["registry", "both"]);
  });

  it("drops malformed entries instead of emitting non-string names", () => {
    expect(imagePullReferenceNames({
      items: [
        null,
        "registry",
        { allowed_uses: ["image_pull"] },
        { name: 42, allowed_uses: ["image_pull"] },
        { name: "no-uses" },
        { name: "ok", allowed_uses: ["image_pull"] },
      ],
    })).toEqual(["ok"]);
  });

  it("returns undefined when the payload has no items array", () => {
    expect(imagePullReferenceNames(null)).toBeUndefined();
    expect(imagePullReferenceNames("nope")).toBeUndefined();
    expect(imagePullReferenceNames({})).toBeUndefined();
    expect(imagePullReferenceNames({ items: "nope" })).toBeUndefined();
  });

  it("returns an empty list for an empty items array", () => {
    expect(imagePullReferenceNames({ items: [] })).toEqual([]);
  });
});
