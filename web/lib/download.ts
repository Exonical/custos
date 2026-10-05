import { extractErrorEnvelope } from "@/lib/api/error-details";

export class BffDownloadError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly details: unknown,
    readonly serverMessage: string,
  ) {
    super(code);
    this.name = "BffDownloadError";
  }
}

function basename(value: string): string {
  let decoded = value;
  try {
    decoded = decodeURIComponent(value);
  } catch {
    decoded = value;
  }
  const filename = decoded.split(/[\\/]/).at(-1)?.replace(/[\u0000-\u001f\u007f]/g, "").trim() ?? "";
  return filename && filename !== "." && filename !== ".." ? filename : "";
}

export function sanitizeDownloadFilename(value: string, fallbackFilename: string): string {
  return basename(value) || basename(fallbackFilename) || "download";
}

export function filenameFromContentDisposition(header: string | null, fallbackFilename: string): string {
  const fallback = sanitizeDownloadFilename("", fallbackFilename);
  if (!header) return fallback;
  const extended = header.match(/(?:^|;)\s*filename\*\s*=\s*(?:UTF-8'')?([^;]+)/i)?.[1]?.trim().replace(/^"|"$/g, "");
  const quoted = header.match(/(?:^|;)\s*filename\s*=\s*"((?:\\.|[^"])*)"/i)?.[1]?.replace(/\\(.)/g, "$1");
  const plain = header.match(/(?:^|;)\s*filename\s*=\s*([^;]+)/i)?.[1]?.trim();
  return sanitizeDownloadFilename(extended ?? quoted ?? plain ?? "", fallback);
}

export async function downloadFromBff(path: string, options: {
  accept: string;
  fallbackFilename: string;
}): Promise<void> {
  const response = await fetch(`/api/bff/${path.replace(/^\/+/, "")}`, {
    method: "GET",
    credentials: "same-origin",
    cache: "no-store",
    headers: { Accept: options.accept },
  });
  if (!response.ok) {
    const payload: unknown = await response.json().catch(() => null);
    const error = extractErrorEnvelope(payload);
    throw new BffDownloadError(
      response.status,
      error.code ?? `HTTP_${String(response.status)}`,
      error.details,
      error.message ?? "",
    );
  }
  const filename = filenameFromContentDisposition(response.headers.get("content-disposition"), options.fallbackFilename);
  const objectUrl = URL.createObjectURL(await response.blob());
  const anchor = document.createElement("a");
  anchor.href = objectUrl;
  anchor.download = filename;
  anchor.style.display = "none";
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => {
    URL.revokeObjectURL(objectUrl);
  }, 0);
}
