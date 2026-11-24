// Unicode White_Space matches the API's trimming rules.
const surroundingWhitespace = /^[\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+|[\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+$/g;

export function parseMachineName(value: string): string | null {
  const name = value.replace(surroundingWhitespace, '');
  if (!name || [...name].length > 100 || /[\0-\x1f\x7f-\x9f\ud800-\udfff]/u.test(name)) return null;
  return name;
}
