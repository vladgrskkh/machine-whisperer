import type { ClaimDeviceRequest } from '../../api/types';
import { parseUUID } from '../../api/uuid';

type DeviceCredentials = Omit<ClaimDeviceRequest, 'machine_id'>;
// The last base64url character has two unused bits for a 32-byte secret.
const claimCode = /^[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/;

export function parseClaimCode(value: unknown): string | null {
  return typeof value === 'string' && value.length === 43 && claimCode.test(value) ? value : null;
}

export function parseClaimQR(raw: string): DeviceCredentials | null {
  if (raw.length > 1024) return null;
  let payload: unknown;
  try {
    payload = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof payload !== 'object' || payload === null
    || !('v' in payload) || payload.v !== 1
    || !('device_id' in payload) || !('claim_code' in payload)
    || Object.keys(payload).length !== 3) return null;

  // JSON.parse collapses duplicate keys. Count the original key tokens as well.
  const keys = [...raw.matchAll(/("(?:[^"\\]|\\.)*")(\s*:)?/g)].filter((token) => token[2]);
  const version = keys.find((token) => JSON.parse(token[1]) === 'v');
  // The provisioned payload uses an integer version, which JSON.parse treats as a number.
  if (keys.length !== 3 || !version || !/^\s*1\s*[,}]/.test(raw.slice(version.index + version[0].length))) return null;

  const deviceId = parseUUID(payload.device_id);
  const code = parseClaimCode(payload.claim_code);
  return deviceId && code ? { device_id: deviceId, claim_code: code } : null;
}
