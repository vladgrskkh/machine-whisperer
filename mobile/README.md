# Machine Whisperer mobile

Expo SDK 58 and TypeScript for iOS and Android. Use Node.js 24.22.3, pinned
in the repository's `.nvmrc`. Run `nvm use` from the repository root if you
use nvm, then run these commands from `mobile/`.

```sh
npm ci
cp .env.example .env.local
npm start
```

Set `EXPO_PUBLIC_API_URL` in `.env.local` to the base URL of a running Machine
Whisperer API. Use an HTTPS endpoint reachable from the device. On a physical
phone, `localhost` refers to the phone itself. Expo includes `EXPO_PUBLIC_`
variables in the client bundle, so put only the public API address there.

Open the app in an SDK 58-compatible Expo Go or development build. Use
`npm run ios` or `npm run android` to start Metro and open an emulator.

Sign up with an email address and a password of 15–128 characters, then sign in.
Account shows the current user's email and account creation date. The app
stores its access and refresh tokens together in SecureStore. If a refresh
fails, sign in again. Sign out clears local credentials and attempts server
revocation while the app is running. If local cleanup needs a retry, the app
shows a retry action, including after restart. An interrupted or offline
revocation request may leave the server session active until it expires.

```sh
npm run lint
npm run typecheck
npm test
npm run export:check
```

The export check bundles the native JavaScript for iOS and Android. Jest covers
the API client, secure session behavior, auth form interactions, protected routes,
and signing out from Account. Check the SecureStore integration on a physical
phone before distributing a native build.
