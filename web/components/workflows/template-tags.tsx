export function TemplateTags({ tags }: { tags: Record<string, string> }) {
  const entries = Object.entries(tags).sort(([left], [right]) => left.localeCompare(right));
  if (entries.length === 0) return null;
  return (
    <ul aria-label="Tags" className="flex flex-wrap gap-1">
      {entries.map(([key, value]) => (
        <li key={key} className="border border-border px-1.5 py-0.5 font-mono text-[9px] text-muted-foreground">
          {key}{value ? `: ${value}` : ""}
        </li>
      ))}
    </ul>
  );
}
