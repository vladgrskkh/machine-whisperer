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

## Tracing

The app records the initial machine history load and its HTTP attempts. The
protected trace endpoint receives a bounded batch on the API's HTTPS origin.
Nginx continues the app's W3C trace context and controls sampling. A sampled
history load appears in Jaeger as one `machine-whisperer` trace rooted at
the app action. A fetch failure can produce a trace on its own.

When Expo Web is enabled, tracing requires the app and API to share an HTTPS origin.
Set `EXPO_PUBLIC_TRACING=0` in the build environment to opt out. The
collector and TLS setup are in the [deployment guide](../deploy/README.md).

## Signed Android APK

Pushing a [`release-X.Y.Z` tag](../deploy/README.md#docker-hub-images) starts the
release CI run. After the checks pass, CI builds and signs the standalone APK
for `https://onerep.mooo.com` from the tagged commit. Pull requests and pushes to
`main` run the mobile lint, type, test, and export checks.

Before the first release build, create the GitHub Actions environment
`android-apk-release` in repository settings. Under **Selected branches and tags**,
add a **Tag** rule for `release-*`. Remove the former `main` branch rule when the
tag workflow lands.

Updates to `com.machinewhisperer.app` require the same signing key. Reuse the
existing key if the app has already been distributed. For a first release, set
`signing_dir` to an owner-only directory outside the repository and save a
strong password as `keystore-password.txt` in that directory, without a trailing
newline.

```bash
signing_dir=/path/to/private/signing-directory
umask 077
keytool -genkeypair -storetype PKCS12 -keystore "$signing_dir/machine-whisperer.p12" \
  -alias machine-whisperer -keyalg RSA -keysize 2048 -validity 10000 \
  -dname 'CN=Machine Whisperer' -storepass:file "$signing_dir/keystore-password.txt" \
  -keypass:file "$signing_dir/keystore-password.txt"
```

Back up the keystore and password in the team's credential store. From a
repository checkout, load the environment secrets over standard input.
The new PKCS12 key uses the same password for both password secrets. When
reusing a key, substitute its keystore file, alias, and password files.

```bash
base64 < "$signing_dir/machine-whisperer.p12" | gh secret set ANDROID_KEYSTORE_BASE64 --env android-apk-release
gh secret set ANDROID_KEYSTORE_PASSWORD --env android-apk-release < "$signing_dir/keystore-password.txt"
printf '%s' machine-whisperer | gh secret set ANDROID_KEY_ALIAS --env android-apk-release
gh secret set ANDROID_KEY_PASSWORD --env android-apk-release < "$signing_dir/keystore-password.txt"
```

1. Open [GitHub Releases](https://github.com/vladgrskkh/machine-whisperer/releases)
   and select the release tag. CI publishes the release after Android signing
   and Docker image promotion pass.
2. Under **Assets**, download `machine-whisperer.apk` and
   `machine-whisperer.apk.sha256` into the same directory. Verify the download
   with `sha256sum -c machine-whisperer.apk.sha256` (or
   `shasum -a 256 -c machine-whisperer.apk.sha256` on macOS).
3. Open the APK on the phone and allow installs from the app that opened it if
   Android prompts you. Launch Machine Whisperer and sign in to check the build.
