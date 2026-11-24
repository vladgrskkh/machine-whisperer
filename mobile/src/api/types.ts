import { parseUUID } from './uuid';

export type Credentials = {
  email: string;
  password: string;
};

export type User = {
  id: string;
  email: string;
  created_at: string;
};

const accountTimestamp = /^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/;
const emailAddress = /^[^\s@]+@[^\s@]+$/;

export function isUser(value: unknown): value is User {
  if (typeof value !== 'object' || value === null) return false;
  const { id, email, created_at } = value as Partial<User>;
  if (parseUUID(id) === null || typeof email !== 'string' || !emailAddress.test(email)
    || typeof created_at !== 'string' || !accountTimestamp.test(created_at)
    || !Number.isFinite(Date.parse(created_at))) return false;

  // Date.parse can roll an impossible calendar date into the next month.
  const day = created_at.slice(0, 10);
  const midnight = new Date(`${day}T00:00:00Z`);
  return Number.isFinite(midnight.getTime()) && midnight.toISOString().startsWith(day);
}

export type TokenPair = {
  access_token: string;
  refresh_token: string;
  token_type: 'Bearer';
  expires_in: number;
};

export function isTokenPair(value: unknown): value is TokenPair {
  if (typeof value !== 'object' || value === null) return false;
  const pair = value as Partial<TokenPair>;
  return typeof pair.access_token === 'string' && pair.access_token.length > 0
    && typeof pair.refresh_token === 'string' && pair.refresh_token.length > 0
    && pair.token_type === 'Bearer'
    && typeof pair.expires_in === 'number' && Number.isFinite(pair.expires_in) && pair.expires_in > 0;
}

export type Machine = {
  id: string;
  name: string;
  created_at: string;
};

export type CreateMachineRequest = { name: string };

export type ClaimDeviceRequest = {
  device_id: string;
  claim_code: string;
  machine_id: string;
};

export type ClaimedDevice = {
  device_id: string;
  machine_id: string;
  status: 'claimed';
};

export type BaselineValues = {
  mean_rms_g: number;
  stddev_rms_g: number;
};

export type MachineBaseline = BaselineValues & {
  machine_id: string;
  version: number;
  updated_at: string;
};

export type TelemetryObservation = {
  device_id: string;
  event_id: string;
  observed_at: string;
  received_at: string;
  schema_version: number;
  operating_state: 'running' | 'unknown';
  vibration_rms_g: number;
  dominant_frequency_hz: number | null;
  current_rms_a: number | null;
};

export type TelemetryPage = {
  items: TelemetryObservation[];
  next_cursor: string | null;
};

export type MachineAlert = {
  id: string;
  machine_id: string;
  trigger_device_id: string;
  trigger_event_id: string;
  triggered_at: string;
  trigger_z_score: number;
  trigger_baseline_version: number;
  trigger_mean_rms_g: number;
  trigger_stddev_rms_g: number;
  last_z_score: number;
  peak_z_score: number;
  recovery_device_id: string | null;
  recovery_event_id: string | null;
  recovered_at: string | null;
  recovery_baseline_version: number | null;
  acknowledged_at: string | null;
};
