import * as SecureStore from 'expo-secure-store';
import { fireEvent, renderRouter, screen, waitFor } from 'expo-router/testing-library';

import AuthLayout from '../../../app/(auth)/_layout';
import SignIn from '../../../app/(auth)/sign-in';
import SignUp from '../../../app/(auth)/sign-up';
import Alerts from '../../../app/(tabs)/alerts';
import Baseline from '../../../app/machines/[id]/baseline';
import MachineDetail from '../../../app/machines/[id]/index';
import Tabs from '../../../app/(tabs)/_layout';
import Account from '../../../app/(tabs)/account';
import Machines from '../../../app/(tabs)/machines/index';
import NewMachine from '../../../app/machines/new';
import ClaimDevice from '../../../app/machines/[id]/claim-device';
import RootLayout from '../../../app/_layout';
import Index from '../../../app/index';

jest.mock('expo-secure-store', () => ({
  getItemAsync: jest.fn(),
  setItemAsync: jest.fn(),
  deleteItemAsync: jest.fn(),
  WHEN_UNLOCKED_THIS_DEVICE_ONLY: 'WHEN_UNLOCKED_THIS_DEVICE_ONLY',
}));

const routes = {
  _layout: RootLayout,
  index: Index,
  '(auth)/_layout': AuthLayout,
  '(auth)/sign-in': SignIn,
  '(auth)/sign-up': SignUp,
  '(tabs)/_layout': Tabs,
  '(tabs)/account': Account,
  '(tabs)/alerts': Alerts,
  '(tabs)/machines/index': Machines,
  'machines/new': NewMachine,
  'machines/[id]/claim-device': ClaimDevice,
  'machines/[id]/index': MachineDetail,
  'machines/[id]/baseline': Baseline,
};
const user = { id: '31766072-1cfc-4798-ac7e-340151697e3c', email: 'operator@example.com', created_at: '2026-11-24T00:00:00Z' };
const pair = { access_token: 'access', refresh_token: 'refresh', token_type: 'Bearer', expires_in: 900 };
const originalBaseUrl = process.env.EXPO_PUBLIC_API_URL;

beforeEach(() => {
  jest.clearAllMocks();
  process.env.EXPO_PUBLIC_API_URL = 'https://example.test';
  jest.mocked(SecureStore.getItemAsync).mockResolvedValue(null);
  jest.mocked(SecureStore.setItemAsync).mockResolvedValue(undefined);
  jest.mocked(SecureStore.deleteItemAsync).mockResolvedValue(undefined);
});

afterEach(() => {
  if (originalBaseUrl === undefined) delete process.env.EXPO_PUBLIC_API_URL;
  else process.env.EXPO_PUBLIC_API_URL = originalBaseUrl;
  jest.restoreAllMocks();
  jest.useRealTimers();
});

async function enterCredentials() {
  await fireEvent.changeText(screen.getByLabelText('Email'), user.email);
  await fireEvent.changeText(screen.getByLabelText('Password'), 'an operator password');
}

it('routes completed sign-up to sign-in without saving a session', async () => {
  const request = jest.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(user), { status: 201 }));
  const navigation = await renderRouter(routes, { initialUrl: '/sign-up' });

  await screen.findByRole('header', { name: 'Create your account' });
  await enterCredentials();
  await fireEvent.press(screen.getByRole('button', { name: 'Create account' }));

  expect(await screen.findByText('Account created. Sign in to continue.')).toBeOnTheScreen();
  expect(navigation.getPathname()).toBe('/sign-in');
  expect(request).toHaveBeenCalledTimes(1);
  expect(request).toHaveBeenCalledWith('https://example.test/v1/auth/signup', expect.objectContaining({ method: 'POST' }));
  expect(SecureStore.setItemAsync).not.toHaveBeenCalled();
});

it('blocks a signed-out account deep link and routes through sign-in, account, and logout', async () => {
  const request = jest.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
    const path = String(url);
    if (path.endsWith('/login')) return new Response(JSON.stringify(pair));
    if (path.endsWith('/machines')) return new Response('[]');
    if (path.endsWith('/me')) return new Response(JSON.stringify(user));
    if (path.endsWith('/logout')) return new Response(null, { status: 204 });
    throw new Error('Unexpected request');
  });
  const navigation = await renderRouter(routes, { initialUrl: '/account' });

  await screen.findByRole('header', { name: 'Sign in' });
  expect(navigation.getPathname()).toBe('/sign-in');
  expect(request).not.toHaveBeenCalled();
  await enterCredentials();
  await fireEvent.press(screen.getByRole('button', { name: 'Sign in' }));

  expect(await screen.findByRole('header', { name: 'Your machines' })).toBeOnTheScreen();
  expect(navigation.getPathname()).toBe('/machines');
  await fireEvent.press(screen.getByRole('button', { name: /^Account, tab/ }));

  expect(await screen.findByText(user.email)).toBeOnTheScreen();
  expect(navigation.getPathname()).toBe('/account');
  expect(SecureStore.setItemAsync).toHaveBeenCalledWith(expect.any(String), JSON.stringify(pair), expect.any(Object));

  await fireEvent.press(screen.getByRole('button', { name: 'Sign out' }));
  await screen.findByRole('header', { name: 'Sign in' });
  expect(navigation.getPathname()).toBe('/sign-in');
  await waitFor(() => expect(SecureStore.deleteItemAsync).toHaveBeenCalledTimes(1));
  expect(request).toHaveBeenCalledWith('https://example.test/v1/auth/logout', expect.objectContaining({ method: 'POST' }));
});

it('hides the protected screen while signing out and offers a retry if secure deletion fails', async () => {
  jest.mocked(SecureStore.getItemAsync).mockResolvedValue(JSON.stringify(pair));
  let rejectDelete!: (error: Error) => void;
  jest.mocked(SecureStore.deleteItemAsync).mockReturnValueOnce(new Promise((_, reject) => {
    rejectDelete = reject;
  }));
  jest.spyOn(globalThis, 'fetch').mockImplementation(async (url) => {
    if (String(url).endsWith('/logout')) return new Response(null, { status: 204 });
    return new Response(JSON.stringify(user));
  });
  await renderRouter(routes, { initialUrl: '/account' });

  await screen.findByText(user.email);
  await fireEvent.press(screen.getByRole('button', { name: 'Sign out' }));
  await waitFor(() => expect(SecureStore.deleteItemAsync).toHaveBeenCalledTimes(1));
  try {
    expect(await screen.findByLabelText('Clearing session')).toBeOnTheScreen();
    expect(screen.queryByText(user.email)).toBeNull();
    expect(screen.queryByRole('header', { name: 'Sign in' })).toBeNull();
  } finally {
    rejectDelete(new Error('Secure storage unavailable'));
  }

  expect(await screen.findByRole('alert')).toHaveTextContent('Could not remove the session from this device.');
  expect(screen.queryByText(user.email)).toBeNull();
  expect(screen.queryByRole('header', { name: 'Sign in' })).toBeNull();
  await fireEvent.press(screen.getByRole('button', { name: 'Try again' }));

  expect(await screen.findByRole('header', { name: 'Sign in' })).toBeOnTheScreen();
  expect(SecureStore.deleteItemAsync).toHaveBeenCalledTimes(2);
});

it('shows a retryable error if the account response is malformed', async () => {
  jest.mocked(SecureStore.getItemAsync).mockResolvedValue(JSON.stringify(pair));
  jest.spyOn(globalThis, 'fetch')
    .mockResolvedValueOnce(new Response(JSON.stringify({ ...user, created_at: 'invalid' })))
    .mockResolvedValueOnce(new Response(JSON.stringify(user)));
  await renderRouter(routes, { initialUrl: '/account' });

  expect(await screen.findByRole('alert')).toHaveTextContent(/unreadable account response/i);
  expect(screen.queryByText('Invalid Date', { exact: false })).toBeNull();
  await fireEvent.press(screen.getByRole('button', { name: 'Try again' }));

  expect(await screen.findByText(user.email)).toBeOnTheScreen();
});
