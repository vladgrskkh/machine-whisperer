import type { ApiClient } from '../../api/client';
import type { Machine, MachineAlert } from '../../api/types';

export type NamedAlert = { machine: Machine; alert: MachineAlert };

export async function loadMachineAlerts(api: ApiClient, signal: AbortSignal) {
  const machines = await api.listMachines();
  const alerts: NamedAlert[] = [];
  let failures = 0;
  let nextMachine = 0;

  async function loadNextMachine() {
    while (!signal.aborted && nextMachine < machines.length) {
      const machine = machines[nextMachine++];
      try {
        const machineAlerts = await api.listAlerts(machine.id, signal);
        for (const alert of machineAlerts) alerts.push({ machine, alert });
      } catch {
        failures++;
      }
    }
  }

  await Promise.all(Array.from({ length: Math.min(4, machines.length) }, loadNextMachine));

  return {
    alerts: alerts.sort((a, b) => Date.parse(b.alert.triggered_at) - Date.parse(a.alert.triggered_at)
      || a.alert.id.localeCompare(b.alert.id)),
    failures,
  };
}
