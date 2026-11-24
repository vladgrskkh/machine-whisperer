import * as Camera from 'expo-camera';
import * as SecureStore from 'expo-secure-store';
import { fireEvent, renderRouter, screen, waitFor } from 'expo-router/testing-library';

import AuthLayout from '../../../../app/(auth)/_layout';
import SignIn from '../../../../app/(auth)/sign-in';
import Tabs from '../../../../app/(tabs)/_layout';
import Account from '../../../../app/(tabs)/account';
import Machines from '../../../../app/(tabs)/machines/index';
import RootLayout from '../../../../app/_layout';
import Index from '../../../../app/index';
import ClaimDevice from '../../../../app/machines/[id]/claim-device';
import NewMachine from '../../../../app/machines/new';

jest.mock('expo-secure-store', () => ({
  getItemAsync: jest.fn(),
  setItemAsync: jest.fn(),
  deleteItemAsync: jest.fn(),
  WHEN_UNLOCKED_THIS_DEVICE_ONLY: 'WHEN_UNLOCKED_THIS_DEVICE_ONLY',
}));

jest.mock('expo-camera', () => {
  const { Pressable } = jest.requireActual('react-native');
  return {
    CameraView: jest.fn(({ onBarcodeScanned }) => (
      <>
        <Pressable accessibilityRole="button" accessibilityLabel="Read sample QR"
          onPress={() => onBarcodeScanned({ data: JSON.stringify({
            v: 1, device_id: '93a68d42-c83f-4221-aeae-d951a995b262', claim_code: 'A'.repeat(43),
          }) })} />
        <Pressable accessibilityRole="button" accessibilityLabel="Read invalid QR"
          onPress={() => onBarcodeScanned({ data: 'unrelated content' })} />
      </>
    )),
    useCameraPermissions: jest.fn(),
    PermissionStatus: { GRANTED: 'granted', DENIED: 'denied' },
  };
});

const routes = {
  _layout: RootLayout,
  index: Index,
  '(auth)/_layout': AuthLayout,
  '(auth)/sign-in': SignIn,
  '(tabs)/_layout': Tabs,
  '(tabs)/account': Account,
  '(tabs)/machines/index': Machines,
  'machines/new': NewMachine,
  'machines/[id]/claim-device': ClaimDevice,
};
const base = 'https://example.test';
const originalBaseUrl = process.env.EXPO_PUBLIC_API_URL;
const pair = { access_token: 'access', refresh_token: 'refresh', token_type: 'Bearer', expires_in: 900 };
const fan = { id: '0bb58342-734e-4587-a1bd-95f9c1e7a302', name: 'Workshop fan', created_at: '2026-11-24T00:00:00Z' };
const pump = { ...fan, id: '7ebac931-e3b1-4a33-829a-13b5d852aaf8', name: 'Water pump' };
const deviceId = '93a68d42-c83f-4221-aeae-d951a995b262';
const claimCode = 'A'.repeat(43);
const claimPath = `/machines/${fan.id}/claim-device`;
const permission = (granted: boolean, canAskAgain = true): Camera.PermissionResponse => ({
  granted,
  canAskAgain,
  expires: 'never',
  status: granted ? Camera.PermissionStatus.GRANTED : Camera.PermissionStatus.DENIED,
});
const requestPermission = jest.fn();
const getPermission = jest.fn();

function json(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), { status });
}

function network(handler?: (path: string, init?: RequestInit) => Response | undefined) {
  return jest.spyOn(globalThis, 'fetch').mockImplementation(async (url, init) => {
    const path = new URL(String(url)).pathname;
    const custom = handler?.(path, init);
    if (custom) return custom;
    if (path === '/v1/machines' && init?.method === 'GET') return json([fan, pump]);
    if (path === `/v1/machines/${fan.id}`) return json(fan);
    if (path === `/v1/machines/${pump.id}`) return json(pump);
    if (path === '/v1/devices/claim') {
      return json({ device_id: deviceId, machine_id: fan.id, status: 'claimed' });
    }
    throw new Error('Unexpected request');
  });
}

function press(name: string) {
  fireEvent.press(screen.getByRole('button', { name }));
}

function enterClaim(id: string, code: string) {
  fireEvent.changeText(screen.getByLabelText('Device ID'), id);
  fireEvent.changeText(screen.getByLabelText('Claim code'), code);
}

beforeEach(() => {
  jest.clearAllMocks();
  process.env.EXPO_PUBLIC_API_URL = base;
  jest.mocked(SecureStore.getItemAsync).mockResolvedValue(JSON.stringify(pair));
  jest.mocked(Camera.useCameraPermissions).mockReturnValue([permission(false), requestPermission, getPermission]);
  requestPermission.mockResolvedValue(permission(false, false));
});

afterEach(() => {
  if (originalBaseUrl === undefined) delete process.env.EXPO_PUBLIC_API_URL;
  else process.env.EXPO_PUBLIC_API_URL = originalBaseUrl;
  jest.restoreAllMocks();
});

it('shows an empty machine list with a single add action', async () => {
  network((path) => path === '/v1/machines' ? json([]) : undefined);
  renderRouter(routes, { initialUrl: '/machines' });

  expect(await screen.findByText('No machines yet.')).toBeOnTheScreen();
  expect(screen.getAllByRole('button', { name: 'Add machine' })).toHaveLength(1);
  expect(screen.queryByRole('button', { name: /scan device qr/i })).toBeNull();
});

it('loads the selected machine before showing the claim form', async () => {
  const fetch = network();
  const navigation = renderRouter(routes, { initialUrl: '/machines' });

  await screen.findByRole('button', { name: fan.name });
  press(pump.name);

  expect(await screen.findByText(/Pair a sensor device with Water pump/)).toBeOnTheScreen();
  expect(navigation.getPathname()).toBe(`/machines/${pump.id}/claim-device`);
  expect(fetch).toHaveBeenLastCalledWith(base + '/v1/machines/' + pump.id, expect.objectContaining({ method: 'GET' }));
});

it('reports a list error and retries loading', async () => {
  let reads = 0;
  network((path) => path === '/v1/machines' && reads++ === 0
    ? json({ error: { code: 'internal_error', message: 'Could not load your machines.' } }, 500)
    : undefined);
  renderRouter(routes, { initialUrl: '/machines' });

  expect(await screen.findByRole('alert')).toHaveTextContent('Could not load your machines.');
  press('Try again');
  expect(await screen.findByRole('button', { name: fan.name })).toBeOnTheScreen();
});

it('creates a machine by normalized name and opens that machine’s claim flow', async () => {
  let created = false;
  const fetch = network((path, init) => {
    if (path !== '/v1/machines') return undefined;
    if (init?.method === 'POST') {
      created = true;
      return json(fan, 201);
    }
    return json(created ? [fan, pump] : [pump]);
  });
  const navigation = renderRouter(routes, { initialUrl: '/machines' });

  await screen.findByRole('button', { name: pump.name });
  expect(screen.queryByRole('button', { name: fan.name })).toBeNull();
  press('Add machine');
  fireEvent.changeText(screen.getByLabelText('Machine name'), '  Workshop fan  ');
  press('Create machine');

  expect(await screen.findByText(/Pair a sensor device with Workshop fan/)).toBeOnTheScreen();
  expect(navigation.getPathname()).toBe(claimPath);
  expect(fetch).toHaveBeenCalledWith(base + '/v1/machines', expect.objectContaining({
    method: 'POST', body: '{"name":"Workshop fan"}',
  }));
  fireEvent.press(screen.getByRole('link', { name: /Machines/ }));
  expect(await screen.findByRole('button', { name: fan.name })).toBeOnTheScreen();
});

it('keeps an invalid name local and shows a public error for a failed creation', async () => {
  const fetch = network(() => json({ error: { code: 'invalid_input', message: 'Invalid machine name or request body' } }, 400));
  renderRouter(routes, { initialUrl: '/machines/new' });

  await screen.findByRole('header', { name: 'Add a machine' });
  fireEvent.changeText(screen.getByLabelText('Machine name'), ' '.repeat(4));
  press('Create machine');
  expect(screen.getByRole('alert')).toHaveTextContent(/machine name/i);
  expect(fetch).not.toHaveBeenCalled();

  fireEvent.changeText(screen.getByLabelText('Machine name'), 'Workshop fan');
  press('Create machine');
  expect(await screen.findByText('Invalid machine name or request body')).toBeOnTheScreen();
  expect(screen.getByLabelText('Machine name')).toHaveDisplayValue('Workshop fan');
});

it('keeps manual entry available after camera denial and binds the request to the selected machine', async () => {
  const fetch = network();
  const navigation = renderRouter(routes, { initialUrl: claimPath });

  await screen.findByRole('button', { name: 'Scan device QR' });
  press('Scan device QR');
  expect(await screen.findByText(/camera access is denied/i)).toBeOnTheScreen();
  press('Enter claim code');
  enterClaim(deviceId.toUpperCase(), claimCode);
  press('Claim device');

  expect(await screen.findByText(/Sensor device connected/)).toBeOnTheScreen();
  expect(navigation.getPathname()).toBe(claimPath);
  expect(fetch).toHaveBeenCalledWith(base + '/v1/devices/claim', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ device_id: deviceId, claim_code: claimCode, machine_id: fan.id }),
  }));
  press('Back to machines');
  expect(await screen.findByRole('header', { name: 'Your machines' })).toBeOnTheScreen();
});

it('recovers after scanning an invalid QR and uses a valid QR for the selected machine', async () => {
  jest.mocked(Camera.useCameraPermissions).mockReturnValue([permission(true), requestPermission, getPermission]);
  const fetch = network();
  renderRouter(routes, { initialUrl: claimPath });

  await screen.findByRole('button', { name: 'Scan device QR' });
  press('Scan device QR');
  press('Read invalid QR');
  expect(screen.getByRole('alert')).toHaveTextContent(/invalid device qr/i);
  expect(fetch).toHaveBeenCalledTimes(1);
  press('Scan again');
  press('Read sample QR');
  expect(screen.getByLabelText('Device ID')).toHaveDisplayValue(deviceId);
  press('Claim device');

  expect(await screen.findByText(/Sensor device connected/)).toBeOnTheScreen();
  expect(fetch).toHaveBeenCalledWith(base + '/v1/devices/claim', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ device_id: deviceId, claim_code: claimCode, machine_id: fan.id }),
  }));
});

it('blocks invalid manual claim fields and keeps the form available after a claim error', async () => {
  const fetch = network((path) => path === '/v1/devices/claim'
    ? json({ error: { code: 'claim_unavailable', message: 'Device claim unavailable' } }, 404)
    : undefined);
  renderRouter(routes, { initialUrl: claimPath });

  await screen.findByRole('button', { name: 'Enter claim code' });
  press('Enter claim code');
  enterClaim('invalid', 'short');
  press('Claim device');
  expect(screen.getByRole('alert')).toHaveTextContent(/valid device ID/i);
  enterClaim(deviceId, 'short');
  press('Claim device');
  expect(screen.getByRole('alert')).toHaveTextContent(/valid claim code/i);
  expect(fetch).toHaveBeenCalledTimes(1);

  enterClaim(deviceId, claimCode);
  press('Claim device');
  expect(await screen.findByRole('alert')).toHaveTextContent(/device claim unavailable/i);
  expect(screen.getByRole('button', { name: 'Claim device' })).toBeEnabled();
  expect(screen.getByLabelText('Device ID')).toHaveDisplayValue(deviceId);
});

it('blocks invalid machine deep links before making any request', async () => {
  const fetch = network();
  renderRouter(routes, { initialUrl: '/machines/bad-id/claim-device' });
  expect(await screen.findByRole('alert')).toHaveTextContent(/invalid machine id/i);
  expect(fetch).not.toHaveBeenCalled();
});

it('blocks claim deep links for signed-out operators', async () => {
  jest.mocked(SecureStore.getItemAsync).mockResolvedValue(null);
  const fetch = network();
  const navigation = renderRouter(routes, { initialUrl: claimPath });
  await waitFor(() => expect(navigation.getPathname()).toBe('/sign-in'));
  expect(fetch).not.toHaveBeenCalled();
});

it('keeps the claim form unavailable when the selected machine cannot be loaded', async () => {
  const fetch = network((path) => path.endsWith(fan.id)
    ? json({ error: { code: 'machine_not_found', message: 'Machine not found' } }, 404)
    : undefined);
  renderRouter(routes, { initialUrl: claimPath });

  expect(await screen.findByRole('alert')).toHaveTextContent('Machine not found');
  expect(screen.queryByRole('button', { name: 'Enter claim code' })).toBeNull();
  expect(fetch).toHaveBeenCalledTimes(1);
});
