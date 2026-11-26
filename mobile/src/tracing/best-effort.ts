export function bestEffort<T>(record: () => T): T | undefined {
  try {
    return record();
  } catch {
    return undefined;
  }
}
