import { z } from "zod";
import type { WorkflowParameter } from "./normalize";

export function buildParameterSchema(parameters: Record<string, WorkflowParameter>) {
  const shape: Record<string, z.ZodType> = {};
  for (const [name, parameter] of Object.entries(parameters)) {
    const allowed = parameter.enum?.length ? parameter.enum : undefined;
    if (parameter.type === "string") {
      let rule = z.string();
      if (parameter.required) rule = rule.min(1, "Required");
      if (parameter.pattern) {
        try {
          rule = rule.regex(new RegExp(parameter.pattern), "Does not match the required pattern");
        } catch {
          // Ignore malformed patterns from untrusted API data.
        }
      }
      if (allowed) rule = rule.refine((value) => allowed.includes(value), "Choose an allowed value");
      shape[name] = parameter.required ? rule : rule.optional();
      continue;
    }

    if (parameter.type === "boolean") {
      const rule = allowed ? z.boolean().refine((value) => allowed.includes(value)) : z.boolean();
      shape[name] = parameter.required ? rule : rule.optional();
      continue;
    }

    let numeric = parameter.type === "integer" ? z.number().int("Must be an integer") : z.number();
    if (parameter.minimum !== undefined) numeric = numeric.min(parameter.minimum, `Minimum is ${String(parameter.minimum)}`);
    if (parameter.maximum !== undefined) numeric = numeric.max(parameter.maximum, `Maximum is ${String(parameter.maximum)}`);
    if (allowed) numeric = numeric.refine((value) => allowed.includes(value), "Choose an allowed value");
    shape[name] = z.preprocess((value) => {
      if (value === "" || value === undefined || value === null) return undefined;
      return typeof value === "string" ? Number(value) : value;
    }, parameter.required ? numeric : numeric.optional());
  }
  return z.object(shape);
}

export function parameterDefaults(parameters: Record<string, WorkflowParameter>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(parameters).map(([name, parameter]) => {
    if (parameter.default !== undefined) return [name, parameter.default];
    if (parameter.type === "boolean") return [name, false];
    if (parameter.type === "string") return [name, ""];
    return [name, ""];
  }));
}
