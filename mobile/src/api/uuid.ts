const uuid = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i;

export function parseUUID(value: unknown): string | null {
  return typeof value === 'string' && value.length === 36 && uuid.test(value) ? value.toLowerCase() : null;
}
