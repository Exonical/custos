import { describe, expect, it } from "vitest";
import { errorText } from "./bff-fetch";

describe("errorText", () => {
  it("prefers field details over the server message", () => {
    expect(errorText({ error: { code: "VALIDATION", message: "invalid", details: [{ field: "name", reason: "required" }] } }, 422, "create workflows"))
      .toBe("name: required");
  });

  it("falls back to the server message, then the code", () => {
    expect(errorText({ error: { code: "CONFLICT", message: "version already published" } }, 409, "create versions")).toBe("version already published");
    expect(errorText({ error: { code: "CONFLICT", message: "" } }, 409, "create versions")).toBe("Request rejected: CONFLICT");
    expect(errorText(null, 500, "create versions")).toBe("Request rejected: HTTP_500");
  });

  it("keeps status-specific messaging", () => {
    expect(errorText({ error: { code: "FORBIDDEN", message: "denied" } }, 403, "create workflows"))
      .toBe("You do not have permission to create workflows in this project.");
    expect(errorText({ error: { code: "WORKFLOW_NAME_TAKEN", message: "taken" } }, 409, "create workflows"))
      .toBe("A workflow with this name already exists in the project.");
  });
});
