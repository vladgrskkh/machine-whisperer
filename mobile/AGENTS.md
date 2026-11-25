# Machine Whisperer mobile

Read the exact versioned Expo documentation at
https://docs.expo.dev/versions/v58.0.0/ before writing code. Use `npx expo install`
for Expo and React Native modules so native packages match SDK 58 and React
Native 0.88. Run `npx expo install --fix` and `npx expo-doctor` after upgrading
the SDK.

- Use TypeScript and Expo Router. Keep route entry points in `app/`, reusable
  UI in `src/ui/`, and API and session behavior in `src/api/` and `src/auth/`.
  Give operator actions and form fields accessible roles and names.
- Send app requests through `ApiClient` to `EXPO_PUBLIC_API_URL`. Type requests
  and responses, handle public error envelopes, and use the authenticated
  request path for protected endpoints. Keep passwords, tokens, and claim
  codes out of logs and URLs.
- Store the token pair as one SecureStore value. Serialize refresh, remove the
  stored pair before sending the refresh request, and persist the rotated pair
  before retrying the protected request. Clear the session after rejected or
  uncertain refresh. Replace stored credentials with a cleanup marker before
  deletion. Keep protected routes blocked until secure cleanup succeeds,
  including after restart. Route successful signup to sign-in because signup
  returns a user.
- Use Jest 30 and React Native Testing Library 14 to exercise screen interactions
  and observable API and session behavior. Await render and event helpers. Mock
  network and OS storage at their boundaries. Run focused screen tests with
  `npm test -- --testPathPatterns=<pattern>`. Follow the Superpowers workflow for
  feature work.
- Use Node.js 24.3 or later on the 24 line (`nvm use` from the repository root)
  and install from the lockfile with `npm ci`. Run `npm run lint`,
  `npm run typecheck`, `npm test`, and `npm run export:check` before committing.
  Lint allows zero warnings. The `mobile` CI job runs all four checks. Keep ESLint
  and its Expo config pinned.
