import { describe, expect, it } from "vitest";
import { buildParameterSchema, parameterDefaults } from "@/lib/workflow/parameters";
import { normalizeWorkflowSpec } from "@/lib/workflow/normalize";

const parameters = normalizeWorkflowSpec({
  spec: {
    parameters: {
      molecule: { type: "string", required: true, pattern: "^[a-z]+$" },
      iterations: { type: "integer", default: 100, minimum: 1, maximum: 10_000 },
      scale: { type: "number", default: 0.5, minimum: 0, maximum: 1 },
      debug: { type: "boolean", default: false },
    },
    tasks: [{ name: "run" }],
  },
}).spec.parameters;

describe("workflow parameter forms", () => {
  it("builds defaults for all supported parameter types", () => {
    expect(parameterDefaults(parameters)).toEqual({ molecule: "", iterations: 100, scale: 0.5, debug: false });
  });

  it("validates required, pattern, numeric bounds, integers, numbers, and booleans", () => {
    const schema = buildParameterSchema(parameters);
    expect(schema.safeParse({ molecule: "water", iterations: 8, scale: 0.75, debug: true }).success).toBe(true);
    expect(schema.safeParse({ molecule: "", iterations: 8, scale: 0.75, debug: true }).success).toBe(false);
    expect(schema.safeParse({ molecule: "bad name", iterations: 8, scale: 0.75, debug: true }).success).toBe(false);
    expect(schema.safeParse({ molecule: "water", iterations: 10_001, scale: 0.75, debug: true }).success).toBe(false);
    expect(schema.safeParse({ molecule: "water", iterations: 1.5, scale: 0.75, debug: true }).success).toBe(false);
    expect(schema.safeParse({ molecule: "water", iterations: 8, scale: 1.5, debug: true }).success).toBe(false);
    expect(schema.safeParse({ molecule: "water", iterations: 8, scale: 0.75, debug: "true" }).success).toBe(false);
  });
});
