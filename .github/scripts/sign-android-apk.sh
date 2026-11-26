#!/usr/bin/env bash
set -euo pipefail

input_apk=$1
keystore=$2
output_apk=$3

: "${APK_KEY_ALIAS:?Configure ANDROID_KEY_ALIAS}"
: "${APK_KEYSTORE_PASSWORD:?Configure ANDROID_KEYSTORE_PASSWORD}"
: "${APK_KEY_PASSWORD:?Configure ANDROID_KEY_PASSWORD}"

build_tools="$ANDROID_HOME/build-tools/$ANDROID_BUILD_TOOLS"
expected_fingerprint=$(keytool -exportcert -keystore "$keystore" -alias "$APK_KEY_ALIAS" \
  -storepass:env APK_KEYSTORE_PASSWORD | sha256sum | cut -d ' ' -f 1)

"$build_tools/zipalign" -c -P 16 4 "$input_apk"
"$build_tools/apksigner" sign --ks "$keystore" --ks-key-alias "$APK_KEY_ALIAS" \
  --ks-pass env:APK_KEYSTORE_PASSWORD --key-pass env:APK_KEY_PASSWORD \
  --debuggable-apk-permitted false --alignment-preserved true \
  --v4-signing-enabled false \
  --out "$output_apk" "$input_apk"
"$build_tools/zipalign" -c -P 16 4 "$output_apk"

certificates=$("$build_tools/apksigner" verify --verbose --print-certs "$output_apk")
signer_count=$(awk '/^Number of signers: / { print $NF }' <<< "$certificates")
if [[ "$signer_count" != 1 ]]; then
  echo "apksigner did not report exactly one APK signer" >&2
  exit 1
fi

# Build Tools 37 prefixes the certificate digest with the signature scheme.
fingerprint=$(awk '/ certificate SHA-256 digest: / { print $NF }' <<< "$certificates")
if [[ "$fingerprint" != "$expected_fingerprint" ]]; then
  echo "APK signer certificate digest does not match the keystore certificate" >&2
  exit 1
fi

badging=$("$build_tools/aapt" dump badging "$output_apk")
if ! grep -q "^package: name='com\.machinewhisperer\.app' " <<< "$badging"; then
  echo "APK does not have the expected application ID" >&2
  exit 1
fi
