import type { MachineAlert, TelemetryObservation } from '../../api/types';

type Point = { x: number; y: number };
type Marker = { id: string; kind: 'trigger' | 'recovery'; x: number; y: number | null };

export function buildHistoryChartData(observations: TelemetryObservation[], baselineMean: number | null, alerts: MachineAlert[]) {
  // Pages are newest-first, with the backend breaking timestamp ties by device and event.
  const readings = observations.slice().reverse().map((reading) => ({ reading, time: Date.parse(reading.observed_at) }));
  const start = readings[0]?.time;
  const end = readings[readings.length - 1]?.time;
  if (start === undefined || end === undefined) return null;

  const maximum = readings.reduce((max, { reading }) => Math.max(max, reading.vibration_rms_g), baselineMean ?? 0);
  const toX = (time: number) => end === start ? 0.5 : (time - start) / (end - start);
  const toY = (value: number) => 1 - value / (maximum || 1);
  const pointByEvent = new Map<string, Point>();
  const points = readings.map(({ reading, time }) => {
    const point = { x: toX(time), y: toY(reading.vibration_rms_g) };
    pointByEvent.set(`${reading.device_id}/${reading.event_id}`, point);
    return point;
  });

  const markers: Marker[] = [];
  function addMarker(alertId: string, kind: Marker['kind'], observedAt: string, device: string | null, event: string | null) {
    const matched = pointByEvent.get(`${device}/${event}`);
    const time = Date.parse(observedAt);
    if (!matched && (time < start || time > end)) return;
    markers.push({ id: `${alertId}:${kind}`, kind, x: matched?.x ?? toX(time), y: matched?.y ?? null });
  }
  for (const alert of alerts) {
    addMarker(alert.id, 'trigger', alert.triggered_at, alert.trigger_device_id, alert.trigger_event_id);
    if (alert.recovered_at !== null) {
      addMarker(alert.id, 'recovery', alert.recovered_at, alert.recovery_device_id, alert.recovery_event_id);
    }
  }

  return { points, start, end, maximum, baselineY: baselineMean === null ? null : toY(baselineMean), markers };
}
