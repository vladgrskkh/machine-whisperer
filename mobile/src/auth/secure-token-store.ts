import * as SecureStore from 'expo-secure-store';

import { isTokenPair, type TokenPair } from '../api/types';
import type { TokenStore } from './session';

const key = 'machine-whisperer.session';
const options = { keychainAccessible: SecureStore.WHEN_UNLOCKED_THIS_DEVICE_ONLY };

export const secureTokenStore: TokenStore = {
  async load() {
    const stored = await SecureStore.getItemAsync(key, options);
    if (stored === null) return null;

    try {
      const value: unknown = JSON.parse(stored);
      if (isTokenPair(value)) return value;
    } catch (error) {
      if (!(error instanceof SyntaxError)) throw error;
    }
    await SecureStore.deleteItemAsync(key, options);
    return null;
  },
  save(pair: TokenPair) {
    return SecureStore.setItemAsync(key, JSON.stringify(pair), options);
  },
  clear() {
    return SecureStore.deleteItemAsync(key, options);
  },
};
