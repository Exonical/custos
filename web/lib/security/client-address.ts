import { isIP } from "node:net";

export function clientAddress(request: Request, trustedProxyHops: number): string | undefined {
  if (!Number.isSafeInteger(trustedProxyHops) || trustedProxyHops <= 0) return undefined;
  const forwardedFor = request.headers.get("x-forwarded-for");
  if (!forwardedFor) return undefined;
  const chain = forwardedFor.split(",").map((entry) => entry.trim());
  const index = chain.length - trustedProxyHops;
  if (index < 0) return undefined;
  const address = chain[index];
  return address && isIP(address) !== 0 ? address : undefined;
}
