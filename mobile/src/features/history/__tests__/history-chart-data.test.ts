import type { MachineAlert, TelemetryObservation } from '../../../api/types';
import { buildHistoryChartData } from '../history-chart-data';

function reading(second: number, rms: number, event: string): TelemetryObservation {
  return {
    device_id: 'sensor', event_id: event, observed_at: `2026-11-24T12:00:0${second}Z`,
    received_at: '2026-11-24T12:00:09Z', schema_version: 1, operating_state: 'running',
    vibration_rms_g: rms, dominant_frequency_hz: null, current_rms_a: null,
  };
}

function alert(observation: TelemetryObservation, recovery?: TelemetryObservation): MachineAlert {
  return {
    id: observation.event_id, machine_id: 'machine',
    trigger_device_id: observation.device_id, trigger_event_id: observation.event_id, triggered_at: observation.observed_at,
    trigger_z_score: 3, trigger_baseline_version: 1, trigger_mean_rms_g: 0.4, trigger_stddev_rms_g: 0.05,
    last_z_score: 3, peak_z_score: 3, recovery_device_id: recovery?.device_id ?? null, recovery_event_id: recovery?.event_id ?? null,
    recovered_at: recovery?.observed_at ?? null, recovery_baseline_version: recovery ? 1 : null, acknowledged_at: null,
  };
}

it('plots real time gaps, includes the baseline in the scale and clips alerts outside loaded history', () => {
  const newest = reading(8, 2, 'latest');
  const middle = reading(4, 1, 'middle');
  const oldest = reading(2, 0, 'oldest');
  const data = buildHistoryChartData([newest, middle, oldest], 4, [
    alert(middle, newest), alert(reading(1, 3, 'outside')),
  ]);

  expect(data?.points).toEqual([{ x: 0, y: 1 }, { x: 1 / 3, y: 0.75 }, { x: 1, y: 0.5 }]);
  expect(data?.baselineY).toBe(0);
  expect(data?.markers).toEqual([
    { id: 'middle:trigger', kind: 'trigger', x: 1 / 3, y: 0.75 },
    { id: 'middle:recovery', kind: 'recovery', x: 1, y: 0.5 },
  ]);
});

it('keeps a single zero reading and a zero baseline plottable', () => {
  const only = reading(4, 0, 'only');
  const data = buildHistoryChartData([only], 0, [alert(only)]);

  expect(data?.points).toEqual([{ x: 0.5, y: 1 }]);
  expect(data?.baselineY).toBe(1);
  expect(data?.markers).toEqual([{ id: 'only:trigger', kind: 'trigger', x: 0.5, y: 1 }]);
});
