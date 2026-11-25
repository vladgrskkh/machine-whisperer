import { userEvent } from '@testing-library/react-native';
import * as SecureStore from 'expo-secure-store';
import { act, fireEvent, renderRouter, screen, waitFor, within } from 'expo-router/testing-library';

import AuthLayout from '../../../../app/(auth)/_layout';
import ClaimDevice from '../../../../app/machines/[id]/claim-device';
import NewMachine from '../../../../app/machines/new';
import SignIn from '../../../../app/(auth)/sign-in';
import Tabs from '../../../../app/(tabs)/_layout';
import Alerts from '../../../../app/(tabs)/alerts';
import Account from '../../../../app/(tabs)/account';
import Machines from '../../../../app/(tabs)/machines/index';
import Root from '../../../../app/_layout';
import Index from '../../../../app/index';
import Baseline from '../../../../app/machines/[id]/baseline';
import Detail from '../../../../app/machines/[id]/index';

jest.mock('expo-secure-store', () => ({
  getItemAsync: jest.fn(),
  setItemAsync: jest.fn(),
  deleteItemAsync: jest.fn(),
  WHEN_UNLOCKED_THIS_DEVICE_ONLY: 'WHEN_UNLOCKED_THIS_DEVICE_ONLY',
}));

const routes = {
  _layout: Root,
  index: Index,
  '(auth)/_layout': AuthLayout,
  '(auth)/sign-in': SignIn,
  '(tabs)/_layout': Tabs,
  '(tabs)/account': Account,
  '(tabs)/alerts': Alerts,
  '(tabs)/machines/index': Machines,
  'machines/new': NewMachine,
  'machines/[id]/index': Detail,
  'machines/[id]/claim-device': ClaimDevice,
  'machines/[id]/baseline': Baseline,
};

const base = 'https://example.test';
const oldBase = process.env.EXPO_PUBLIC_API_URL;
const machine = { id: '0bb58342-734e-4587-a1bd-95f9c1e7a302', name: 'Workshop fan', created_at: '2026-11-24T00:00:00Z' };
const root = '/v1/machines/' + machine.id;
const detailPath = '/machines/' + machine.id;
const deviceId = '77777777-7777-4777-8777-777777777777';
const latest = {
  device_id: deviceId, event_id: '88888888-8888-4888-8888-888888888888', schema_version: 1,
  received_at: '2026-11-24T12:01:00Z', observed_at: '2026-11-24T12:00:00Z',
  operating_state: 'running', vibration_rms_g: 0.55, dominant_frequency_hz: null, current_rms_a: null,
};
const older = {
  ...latest, event_id: '99999999-9999-4999-8999-999999999999', observed_at: '2026-11-24T11:00:00Z', vibration_rms_g: 0.4,
};
const configured = {
  machine_id: machine.id, mean_rms_g: 0.4, stddev_rms_g: 0.05, version: 1, updated_at: '2026-11-23T12:00:00Z',
};
const active = {
  id: '37cd688f-cca8-4680-b961-cac4a347741d', machine_id: machine.id,
  trigger_device_id: deviceId, trigger_event_id: latest.event_id, triggered_at: latest.observed_at,
  trigger_z_score: 3, trigger_baseline_version: 1, trigger_mean_rms_g: 0.4, trigger_stddev_rms_g: 0.05,
  last_z_score: 3.5, peak_z_score: 4.8, recovery_device_id: null, recovery_event_id: null, recovered_at: null,
  recovery_baseline_version: null, acknowledged_at: null,
};
const recovered = {
  ...active, id: '0be7a07c-d7c8-409c-83f9-05be59e81027', triggered_at: older.observed_at, trigger_event_id: older.event_id,
  recovery_device_id: deviceId, recovery_event_id: latest.event_id, recovered_at: latest.observed_at,
  recovery_baseline_version: 1, acknowledged_at: latest.observed_at,
};

function json(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status });
}

function network(handler?: (url: URL, request?: RequestInit) => Response | Promise<Response> | undefined) {
  return jest.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
    const parsed = new URL(String(url));
    const response = handler?.(parsed, request);
    if (response) return response;
    switch (parsed.pathname) {
      case '/v1/machines': return json([machine]);
      case root: return json(machine);
      case root + '/telemetry': return json({ items: [latest], next_cursor: null });
      case root + '/baseline': return json(configured);
      case root + '/alerts': return json([active, recovered]);
      default: throw new Error('Unexpected request');
    }
  });
}

async function press(name: string | RegExp) {
  await fireEvent.press(screen.getByRole('button', { name }));
}

beforeEach(() => {
  process.env.EXPO_PUBLIC_API_URL = base;
  jest.mocked(SecureStore.getItemAsync).mockResolvedValue(JSON.stringify({
    access_token: 'access', refresh_token: 'refresh', token_type: 'Bearer', expires_in: 900,
  }));
});

afterEach(() => {
  if (oldBase === undefined) delete process.env.EXPO_PUBLIC_API_URL;
  else process.env.EXPO_PUBLIC_API_URL = oldBase;
  jest.restoreAllMocks();
});

it('keeps machines visible while refetching after a tab switch', async () => {
  const { promise: refresh, resolve } = Promise.withResolvers<Response>();
  let listRequests = 0;
  network((url) => {
    if (url.pathname === '/v1/machines' && ++listRequests === 2) return refresh;
    return undefined;
  });
  await renderRouter(routes, { initialUrl: '/machines' });

  await screen.findByText('0.55 g RMS');
  await press(/^Account, tab/);
  await press(/^Machines, tab/);
  await waitFor(() => expect(listRequests).toBe(2));

  expect(screen.getByRole('button', { name: machine.name })).toBeEnabled();
  expect(screen.getByText('0.55 g RMS')).toBeOnTheScreen();
  expect(screen.getByRole('status')).toHaveTextContent(/refreshing machines/i);
  expect(screen.queryByLabelText('Loading machines')).toBeNull();

  await act(async () => { resolve(json([{ ...machine, name: 'Renamed fan' }])); });
  expect(await screen.findByRole('button', { name: 'Renamed fan' })).toBeOnTheScreen();
  expect(screen.queryByRole('button', { name: machine.name })).toBeNull();
  expect(screen.queryByRole('status')).toBeNull();
});

it('keeps loaded alerts and offers a retry when refreshing fails', async () => {
  const { promise: refresh, resolve } = Promise.withResolvers<Response>();
  let listRequests = 0;
  network((url) => {
    if (url.pathname !== '/v1/machines') return undefined;
    if (++listRequests === 2) return refresh;
    if (listRequests === 3) return json([]);
    return undefined;
  });
  await renderRouter(routes, { initialUrl: '/alerts' });

  await screen.findByRole('button', { name: /Acknowledge alert for Workshop fan/ });
  await press(/^Account, tab/);
  await press(/^Alerts, tab/);
  await waitFor(() => expect(listRequests).toBe(2));
  expect(screen.getByRole('button', { name: /Acknowledge alert for Workshop fan/ })).toBeEnabled();
  expect(screen.getByRole('status')).toHaveTextContent(/refreshing alerts/i);

  await act(async () => {
    resolve(json({ error: { code: 'machines_unavailable', message: 'Machine list unavailable' } }, 503));
  });
  expect(await screen.findByRole('alert')).toHaveTextContent('Machine list unavailable');
  expect(screen.getByRole('button', { name: /Acknowledge alert for Workshop fan/ })).toBeEnabled();
  expect(screen.queryByRole('status')).toBeNull();

  await press('Retry alerts refresh');
  expect(await screen.findByText('No active alerts.')).toBeOnTheScreen();
  expect(screen.queryByRole('alert')).toBeNull();
});

it('keeps showing machine history until new observations arrive after returning to detail', async () => {
  const { promise: refresh, resolve } = Promise.withResolvers<Response>();
  let historyRequests = 0;
  network((url) => {
    if (url.pathname === root + '/telemetry' && ++historyRequests >= 2) return refresh;
    return undefined;
  });
  await renderRouter(routes, { initialUrl: detailPath });

  expect(await screen.findByText('0.55 g')).toBeOnTheScreen();
  await press('Edit baseline');
  await screen.findByRole('header', { name: 'Set baseline' });
  await fireEvent.press(await screen.findByRole('link', { name: /Workshop fan/ }));
  await waitFor(() => expect(historyRequests).toBeGreaterThan(1));

  expect(screen.getByText('0.55 g')).toBeOnTheScreen();
  expect(screen.getByRole('status', { name: /refreshing vibration history/i })).toBeOnTheScreen();
  expect(screen.queryByLabelText('Loading vibration history')).toBeNull();

  await act(async () => { resolve(json({ items: [{ ...latest, vibration_rms_g: 0.7 }], next_cursor: null })); });
  expect(await screen.findByText('0.7 g')).toBeOnTheScreen();
  expect(screen.queryByText('0.55 g')).toBeNull();
  expect(screen.queryByRole('status', { name: /refreshing vibration history/i })).toBeNull();
});

it('opens machine detail from Machines and reads older observations with the opaque cursor', async () => {
  const cursor = 'token+/=?& skip';
  const queries: [string | null, string | null][] = [];
  const fetch = network((url) => {
    if (url.pathname !== root + '/telemetry' || url.searchParams.get('limit') === '1') return undefined;
    const supplied = url.searchParams.get('cursor');
    queries.push([url.searchParams.get('limit'), supplied]);
    return supplied === cursor ? json({ items: [older], next_cursor: null }) : json({ items: [latest], next_cursor: cursor });
  });
  const navigation = await renderRouter(routes, { initialUrl: '/machines' });

  await screen.findByRole('button', { name: machine.name });
  await press(machine.name);
  expect(await screen.findByRole('header', { name: machine.name })).toBeOnTheScreen();
  expect(navigation.getPathname()).toBe(detailPath);
  expect(await screen.findByText('1 observation loaded')).toBeOnTheScreen();
  expect(screen.getByText('0.55 g')).toBeOnTheScreen();
  expect(await screen.findByText('Baseline mean')).toBeOnTheScreen();
  expect(await screen.findByText('Alert opened')).toBeOnTheScreen();
  expect(screen.queryByText(/Dominant frequency|Current RMS|null/)).toBeNull();
  expect(screen.getByText(deviceId)).toBeOnTheScreen();

  await press('Load older observations');
  expect(await screen.findByText('2 observations loaded')).toBeOnTheScreen();
  expect(screen.getByText('0.55 g')).toBeOnTheScreen();
  expect(screen.queryByRole('button', { name: 'Load older observations' })).toBeNull();
  expect(queries).toEqual([['100', null], ['100', cursor]]);
  expect(fetch).toHaveBeenCalledWith(expect.any(String), expect.objectContaining({
    headers: expect.objectContaining({ Authorization: 'Bearer access' }),
  }));
});

it('recognizes an empty baseline and retries a failed older page without losing the latest reading', async () => {
  let olderAttempts = 0;
  network((url) => {
    if (url.pathname === root + '/baseline') return json({ error: { code: 'baseline_not_found', message: 'Baseline not found' } }, 404);
    if (url.pathname !== root + '/telemetry') return undefined;
    if (!url.searchParams.has('cursor')) return json({ items: [latest], next_cursor: 'older' });
    if (olderAttempts++ === 0) return json({ error: { code: 'history_unavailable', message: 'History unavailable' } }, 503);
    return json({ items: [older], next_cursor: null });
  });
  await renderRouter(routes, { initialUrl: detailPath });

  expect(await screen.findByText('No baseline set.')).toBeOnTheScreen();
  await screen.findByRole('button', { name: 'Load older observations' });
  await press('Load older observations');
  expect(await screen.findByRole('alert')).toHaveTextContent('History unavailable');
  expect(screen.getByText('0.55 g')).toBeOnTheScreen();
  await press('Load older observations');
  expect(await screen.findByText('2 observations loaded')).toBeOnTheScreen();
});

it('preloads a baseline, validates both fields locally and returns to detail after saving', async () => {
  let saved = configured;
  const fetch = network((url, request) => {
    if (url.pathname !== root + '/baseline') return undefined;
    if (request?.method === 'PUT') saved = { ...configured, ...JSON.parse(String(request.body)), version: 2 };
    return json(saved);
  });
  const navigation = await renderRouter(routes, { initialUrl: detailPath + '/baseline' });
  const mean = 'Baseline mean RMS (g)';
  const stddev = 'Baseline standard deviation (g)';

  expect(await screen.findByLabelText(mean)).toHaveDisplayValue('0.4');
  expect(screen.getByLabelText(stddev)).toHaveDisplayValue('0.05');
  expect(screen.queryByText(/current alert policy|3 consecutive readings/i)).toBeNull();
  for (const invalid of [' ', '-1', 'Infinity', '0x10']) {
    await fireEvent.changeText(screen.getByLabelText(mean), invalid);
    await press('Save baseline');
    expect(screen.getByRole('alert')).toHaveTextContent(/mean/i);
  }
  await fireEvent.changeText(screen.getByLabelText(mean), '0');
  for (const invalid of ['0', '-0.1', '1e309']) {
    await fireEvent.changeText(screen.getByLabelText(stddev), invalid);
    await press('Save baseline');
    expect(screen.getByRole('alert')).toHaveTextContent(/standard deviation/i);
  }
  expect(fetch.mock.calls.filter(([, request]) => request?.method === 'PUT')).toHaveLength(0);

  await fireEvent.changeText(screen.getByLabelText(stddev), ' 0.08 ');
  await press('Save baseline');
  expect(await screen.findByText('Mean 0 g · Std dev 0.08 g')).toBeOnTheScreen();
  expect(navigation.getPathname()).toBe(detailPath);
  expect(fetch).toHaveBeenCalledWith(base + root + '/baseline', expect.objectContaining({
    method: 'PUT', body: '{"mean_rms_g":0,"stddev_rms_g":0.08}',
  }));
});

it('opens a blank editor for a machine without an existing baseline', async () => {
  network((url) => url.pathname === root + '/baseline'
    ? json({ error: { code: 'baseline_not_found', message: 'Baseline not found' } }, 404)
    : undefined);
  await renderRouter(routes, { initialUrl: detailPath + '/baseline' });

  expect(await screen.findByLabelText('Baseline mean RMS (g)')).toHaveDisplayValue('');
  expect(screen.getByLabelText('Baseline standard deviation (g)')).toHaveDisplayValue('');
  expect(screen.queryByRole('alert')).toBeNull();
});

it('aggregates per-machine alerts with at most four requests in flight', async () => {
  const machines = Array.from({ length: 7 }, (_, index) => ({
    ...machine,
    id: `00000000-0000-4000-8000-00000000000${index}`,
    name: `Pump ${index + 1}`,
  }));
  let peak = 0;
  const pending = new Map<string, (response: Response) => void>();
  const requested: string[] = [];
  network((url) => {
    if (url.pathname === '/v1/machines') return json(machines);
    if (!url.pathname.endsWith('/alerts')) return undefined;
    const id = url.pathname.split('/')[3];
    requested.push(id);
    return new Promise((resolve) => {
      pending.set(id, resolve);
      peak = Math.max(peak, pending.size);
    });
  });

  await renderRouter(routes, { initialUrl: '/alerts' });
  await waitFor(() => expect(pending.size).toBe(4));
  expect(screen.queryByRole('button', { name: /^History, tab/ })).toBeNull();
  expect(requested).toHaveLength(4);
  const releaseBatch = async () => {
    await act(async () => {
      const batch = Array.from(pending);
      pending.clear();
      for (const [id, resolve] of batch) {
        const machineIndex = machines.findIndex((item) => item.id === id);
        resolve(json([{ ...active, id: id.replace('0000-', '1111-'), machine_id: id, triggered_at: `2026-11-24T12:0${machineIndex}:00Z` }]));
      }
    });
  };
  await releaseBatch();
  await waitFor(() => expect(pending.size).toBe(3));
  await releaseBatch();

  const activeGroup = await screen.findByLabelText('Active alerts');
  expect(within(activeGroup).getAllByText(/^Pump /).map((item) => item.props.children)).toEqual(
    ['Pump 7', 'Pump 6', 'Pump 5', 'Pump 4', 'Pump 3', 'Pump 2', 'Pump 1'],
  );
  expect(peak).toBe(4);
});

it('keeps an acknowledged alert active and groups recovery separately', async () => {
  const acknowledge = '/v1/alerts/' + active.id + '/acknowledge';
  let attempts = 0;
  const fetch = network((url) => {
    if (url.pathname !== acknowledge) return undefined;
    if (attempts++ === 0) return json({ error: { code: 'internal_error', message: 'Could not acknowledge' } }, 500);
    return new Response(null, { status: 204 });
  });
  await renderRouter(routes, { initialUrl: '/alerts' });
  const activeGroup = await screen.findByLabelText('Active alerts');
  expect(within(await screen.findByLabelText('Recovered alerts')).getByText('Recovered')).toBeOnTheScreen();
  await fireEvent.press(within(activeGroup).getByRole('button', { name: /Acknowledge alert for Workshop fan/ }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not acknowledge');
  await fireEvent.press(within(activeGroup).getByRole('button', { name: /Acknowledge alert for Workshop fan/ }));

  expect(await within(activeGroup).findByText('Seen')).toBeOnTheScreen();
  expect(within(activeGroup).getByText('Active')).toBeOnTheScreen();
  expect(within(activeGroup).queryByRole('button', { name: /Acknowledge/ })).toBeNull();
  expect(fetch).toHaveBeenCalledWith(base + acknowledge, expect.objectContaining({ method: 'POST' }));
});

it.each(['/machines/invalid', '/machines/invalid/baseline'])('blocks invalid machine links at %s', async (path) => {
  const fetch = network();
  await renderRouter(routes, { initialUrl: path });

  expect(await screen.findByRole('alert')).toHaveTextContent(/invalid machine id/i);
  expect(fetch).not.toHaveBeenCalled();
});

it('preserves an in-flight acknowledgement when switching tabs', async () => {
  let resolveAck!: (response: Response) => void;
  let attempts = 0;
  network((url) => {
    if (!url.pathname.endsWith('/acknowledge')) return undefined;
    attempts++;
    return new Promise((resolve) => { resolveAck = resolve; });
  });
  await renderRouter(routes, { initialUrl: '/alerts' });

  await screen.findByLabelText('Active alerts');
  await userEvent.setup().press(screen.getByRole('button', { name: /Acknowledge alert for Workshop fan/ }));
  await press(/^Machines, tab/);
  await screen.findByRole('button', { name: machine.name });
  await press(/^Alerts, tab/);
  await screen.findByLabelText('Active alerts');
  expect(screen.getByRole('button', { name: /Acknowledge alert for Workshop fan/ })).toBeDisabled();

  await act(async () => { resolveAck(new Response(null, { status: 204 })); });
  expect(await within(screen.getByLabelText('Active alerts')).findByText('Seen')).toBeOnTheScreen();
  expect(attempts).toBe(1);
});

it('shows successful machine alerts when another machine has a loading error', async () => {
  const pump = { ...machine, id: '7ebac931-e3b1-4a33-829a-13b5d852aaf8', name: 'Water pump' };
  let attempts = 0;
  network((url) => {
    if (url.pathname === '/v1/machines') return json([machine, pump]);
    if (url.pathname !== `/v1/machines/${pump.id}/alerts`) return undefined;
    if (attempts++ === 0) return json({ error: { code: 'internal_error', message: 'Unavailable' } }, 500);
    return json([]);
  });
  await renderRouter(routes, { initialUrl: '/alerts' });

  expect(await screen.findByRole('alert')).toHaveTextContent('Could not load alerts for 1 machine.');
  expect(within(screen.getByLabelText('Active alerts')).getByText(machine.name)).toBeOnTheScreen();
  await press('Try again');
  await waitFor(() => expect(attempts).toBe(2));
  expect(await screen.findByLabelText('Active alerts')).toBeOnTheScreen();
  expect(screen.queryByRole('alert')).toBeNull();
});

it.each(['/machines', '/machines/0bb58342-734e-4587-a1bd-95f9c1e7a302', '/machines/0bb58342-734e-4587-a1bd-95f9c1e7a302/baseline', '/alerts'])(
  'blocks signed-out access and sends no request at %s', async (path) => {
    jest.mocked(SecureStore.getItemAsync).mockResolvedValue(null);
    const fetch = network();
    const navigation = await renderRouter(routes, { initialUrl: path });

    await waitFor(() => expect(navigation.getPathname()).toBe('/sign-in'));
    expect(fetch).not.toHaveBeenCalled();
  },
);

it('summarizes vibration, baseline and active alerts with a total of at most four machine reads', async () => {
  const pump = { ...machine, id: '7ebac931-e3b1-4a33-829a-13b5d852aaf8', name: 'Water pump' };
  const pending = new Map<string, () => void>();
  let peak = 0;
  const telemetryLimits: (string | null)[] = [];
  network((url) => {
    if (url.pathname === '/v1/machines') return json([machine, pump]);
    const endpoint = url.pathname.split('/').at(-1);
    const isPump = url.pathname.includes(pump.id);
    let response: Response;
    switch (endpoint) {
      case 'telemetry':
        telemetryLimits.push(url.searchParams.get('limit'));
        response = json({ items: isPump ? [] : [latest], next_cursor: null });
        break;
      case 'baseline':
        response = isPump ? json({ error: { code: 'baseline_not_found', message: 'Baseline not found' } }, 404) : json(configured);
        break;
      case 'alerts':
        response = json(isPump ? [] : [active, recovered]);
        break;
      default:
        return undefined;
    }
    return new Promise((resolve) => {
      pending.set(url.pathname, () => resolve(response));
      peak = Math.max(peak, pending.size);
    });
  });
  await renderRouter(routes, { initialUrl: '/machines' });

  expect(await screen.findByRole('button', { name: machine.name })).toBeOnTheScreen();
  await waitFor(() => expect(pending.size).toBe(4));
  const release = async () => {
    await act(async () => {
      const batch = [...pending.values()];
      pending.clear();
      batch.forEach((finish) => finish());
    });
  };
  await release();
  await waitFor(() => expect(pending.size).toBe(2));
  await release();

  const fanCard = screen.getByRole('button', { name: machine.name });
  expect(await within(fanCard).findByText('0.55 g RMS')).toBeOnTheScreen();
  expect(within(fanCard).getByText(/^Observed /)).toBeOnTheScreen();
  expect(within(fanCard).getByText('Baseline set')).toBeOnTheScreen();
  expect(within(fanCard).getByText('Active alert')).toBeOnTheScreen();
  const pumpCard = screen.getByRole('button', { name: pump.name });
  expect(within(pumpCard).getByText('No baseline set')).toBeOnTheScreen();
  expect(within(pumpCard).getByText('No active alerts')).toBeOnTheScreen();
  expect(within(pumpCard).getByText('No sensor readings yet')).toBeOnTheScreen();
  expect(telemetryLimits).toEqual(['1', '1']);
  expect(peak).toBe(4);
});
