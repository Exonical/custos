import { detailText, extractErrorEnvelope } from "@/lib/api/error-details";
import { getJson, sendDelete, sendJson } from "@/lib/api/bff-fetch";
import { downloadFromBff } from "@/lib/download";
import { interpretSave, issuesFromDetails, type NodeConfig, type NodeStatusList, type NodeToken, type SaveOutcome } from "@/lib/nodehooks/config";

const base = (cluster: string) => `/api/bff/clusters/${encodeURIComponent(cluster)}`;

export async function saveNodeConfig(cluster: string, version: number, config: NodeConfig, csrfToken: string): Promise<SaveOutcome> {
  try {
    const response = await sendJson("PUT", `${base(cluster)}/node-config`, { config, version }, csrfToken);
    return interpretSave(response.status, response.payload);
  } catch {
    return { kind: "error", message: "The request could not be sent. Check your connection and try again." };
  }
}

export type CreatedToken = { id: string; name: string; token: string };
export type CreateTokenResult =
  | { ok: true; created: CreatedToken }
  | { ok: false; message: string };

export function interpretCreateToken(status: number, payload: unknown): CreateTokenResult {
  if (status === 201 && payload && typeof payload === "object") {
    const { id, name, token } = payload as { id?: unknown; name?: unknown; token?: unknown };
    if (typeof id === "string" && typeof name === "string" && typeof token === "string") {
      return { ok: true, created: { id, name, token } };
    }
  }
  const error = extractErrorEnvelope(payload);
  const details = error.details ? detailText(issuesFromDetails(error.details).map((issue) => issue)) : "";
  if (status === 403) return { ok: false, message: "You need cluster.manage permission to create node tokens." };
  return { ok: false, message: details || error.message || error.code || `Request failed (HTTP ${String(status)}).` };
}

export async function createNodeToken(cluster: string, name: string, csrfToken: string): Promise<CreateTokenResult> {
  try {
    const response = await sendJson("POST", `${base(cluster)}/node-tokens`, { name }, csrfToken);
    return interpretCreateToken(response.status, response.payload);
  } catch {
    return { ok: false, message: "The request could not be sent. Check your connection and try again." };
  }
}

export async function revokeNodeToken(cluster: string, id: string, csrfToken: string): Promise<string | null> {
  try {
    const response = await sendDelete(`${base(cluster)}/node-tokens/${encodeURIComponent(id)}`, csrfToken);
    if (response.status === 204 || response.status === 200) return null;
    const error = extractErrorEnvelope(response.payload);
    return error.message ?? error.code ?? `Request failed (HTTP ${String(response.status)}).`;
  } catch {
    return "The request could not be sent. Check your connection and try again.";
  }
}

export async function loadNodeTokens(cluster: string): Promise<NodeToken[] | null> {
  try {
    const response = await getJson(`${base(cluster)}/node-tokens`);
    const items = (response.payload as { items?: unknown } | null)?.items;
    return response.status === 200 && Array.isArray(items) ? items as NodeToken[] : null;
  } catch {
    return null;
  }
}

export async function loadNodeStatus(cluster: string): Promise<NodeStatusList | null> {
  try {
    const response = await getJson(`${base(cluster)}/node-status`);
    const payload = response.payload as Partial<NodeStatusList> | null;
    return response.status === 200 && payload && Array.isArray(payload.items) ? payload as NodeStatusList : null;
  } catch {
    return null;
  }
}

export function downloadNodeBundle(cluster: string, clusterName: string, revision: number): Promise<void> {
  return downloadFromBff(`clusters/${encodeURIComponent(cluster)}/node-config/bundle`, {
    accept: "application/gzip",
    fallbackFilename: `custos-node-${clusterName}-r${String(revision)}.tar.gz`,
  });
}
