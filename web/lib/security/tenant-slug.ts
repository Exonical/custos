const TENANT_SLUG = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

export function isTenantSlug(value: string): boolean {
  return TENANT_SLUG.test(value);
}
