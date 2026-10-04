"use client";

import { useMemo } from "react";
import { zodResolver } from "@hookform/resolvers/zod";
import { useForm, type FieldValues, type Path, type UseFormReturn } from "react-hook-form";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { buildParameterSchema, parameterDefaults } from "@/lib/workflow/parameters";
import type { WorkflowParameter } from "@/lib/workflow/normalize";

export function useWorkflowParameterForm(parameters: Record<string, WorkflowParameter>) {
  const schema = useMemo(() => buildParameterSchema(parameters), [parameters]);
  const defaults = useMemo(() => parameterDefaults(parameters), [parameters]);
  const form = useForm({ resolver: zodResolver(schema), defaultValues: defaults });
  return { form, defaults };
}

export function workflowParameterValues(
  values: Record<string, unknown>,
  parameters: Record<string, WorkflowParameter>,
): Record<string, unknown> {
  return Object.fromEntries(Object.entries(values).filter(([name, value]) =>
    !(value === "" && parameters[name].required === false)));
}

export function WorkflowParameterFields<T extends FieldValues>({
  form,
  parameters,
  idPrefix,
}: {
  form: UseFormReturn<T>;
  parameters: Record<string, WorkflowParameter>;
  idPrefix: string;
}) {
  return (
    <>
      {Object.entries(parameters).map(([name, parameter]) => {
        const fieldId = `${idPrefix}-${name}`;
        const fieldName = name as Path<T>;
        const error = form.formState.errors[fieldName]?.message;
        if (parameter.type === "boolean") {
          return (
            <div key={name} className="grid gap-1.5">
              <label htmlFor={fieldId} className="flex items-center gap-2 font-mono text-[10px] uppercase tracking-[0.08em]">
                <input id={fieldId} type="checkbox" {...form.register(fieldName)} className="size-4 accent-primary" />
                {name}{parameter.required ? " · required" : ""}
              </label>
              {typeof error === "string" ? <p role="alert" className="font-mono text-xs text-destructive">{error}</p> : null}
            </div>
          );
        }
        const numeric = parameter.type === "integer" || parameter.type === "number";
        return (
          <div key={name} className="grid gap-1.5">
            <Label htmlFor={fieldId}>{name}{parameter.required ? " · required" : ""}</Label>
            <Input
              id={fieldId}
              type={numeric ? "number" : "text"}
              step={parameter.type === "integer" ? "1" : parameter.type === "number" ? "any" : undefined}
              min={parameter.minimum}
              max={parameter.maximum}
              {...form.register(fieldName)}
            />
            {typeof error === "string" ? <p role="alert" className="font-mono text-xs text-destructive">{error}</p> : null}
          </div>
        );
      })}
    </>
  );
}
