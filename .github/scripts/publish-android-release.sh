#!/usr/bin/env bash
set -euo pipefail

signed_directory=${1:?Pass the signed artifact directory}
: "${RELEASE_COMMIT:?}" "${GITHUB_REPOSITORY:?}" "${GITHUB_REF:?}" "${GITHUB_REF_NAME:?}"
: "${GH_TOKEN:?}"

verify_release_ref() {
    local refs current_commit
    refs=$(git -c credential.helper= -c 'credential.helper=!gh auth git-credential' \
        ls-remote origin "$GITHUB_REF" "${GITHUB_REF}^{}")
    current_commit=$(awk -v ref="$GITHUB_REF" '
        $2 == ref { tag = $1 }
        $2 == ref "^{}" { peeled = $1 }
        END { print (peeled != "" ? peeled : tag) }
    ' <<< "$refs")
    if [[ "$current_commit" != "$RELEASE_COMMIT" ]]; then
        echo 'The release tag changed after this workflow started.' >&2
        exit 1
    fi
}

release_assets=$(mktemp -d)
trap 'rm -rf -- "$release_assets"' EXIT
(
    cd "$signed_directory"
    source_apk="machine-whisperer-$RELEASE_COMMIT.apk"
    original_checksum=$(sha256sum "$source_apk")
    if ! printf '%s\n' "$original_checksum" | cmp -s - "$source_apk.sha256"; then
        echo 'The signed artifact checksum does not match the APK from this commit.' >&2
        exit 1
    fi
    cp "$source_apk" "$release_assets/machine-whisperer.apk"
    cd "$release_assets"
    sha256sum machine-whisperer.apk > machine-whisperer.apk.sha256
)

releases_api="repos/$GITHUB_REPOSITORY/releases"
draft_marker="<!-- machine-whisperer-ci release-commit $RELEASE_COMMIT -->"
find_release() {
    gh api --paginate "$releases_api?per_page=100" |
        jq -cs --arg tag "$GITHUB_REF_NAME" '[.[][] | select(.tag_name == $tag)] |
            if length > 1 then error("Multiple releases reference the tag") else first // empty end'
}

verify_release_ref
release=$(find_release)
if [[ -z "$release" ]]; then
    gh release create "$GITHUB_REF_NAME" --repo "$GITHUB_REPOSITORY" --verify-tag --draft \
        --title "Machine Whisperer ${GITHUB_REF_NAME#release-}" --notes "$draft_marker" --generate-notes
    release=$(find_release)
fi
release_api="$releases_api/$(jq -er '.id' <<< "$release")"
upload_url=$(jq -er '.upload_url' <<< "$release")
upload_url=${upload_url%%\{*}
if [[ "$upload_url" != "https://uploads.github.com/$release_api/assets" ]]; then
    echo 'The release returned an unexpected asset upload URL.' >&2
    exit 1
fi

is_owned_draft() {
    jq -e --arg marker "$draft_marker" \
        '.draft == true and ((.body // "") | . == $marker or startswith($marker + "\n"))' \
        <<< "$1" >/dev/null
}

read_checked_release() {
    local current
    current=$(gh api "$release_api")
    if ! jq -e --arg tag "$GITHUB_REF_NAME" '.tag_name == $tag' <<< "$current" >/dev/null; then
        echo 'The release no longer references the checked tag.' >&2
        exit 1
    fi
    printf '%s\n' "$current"
}

list_assets() {
    gh api --paginate "$release_api/assets?per_page=100" | jq -cs '[.[][]]'
}

find_asset() {
    jq -c --arg name "$1" '[.[] | select(.name == $name)] | first // empty' <<< "$assets"
}

is_empty_starter() {
    jq -e --arg name "$1" '.name == $name and .state == "starter" and .size == 0' <<< "$2" >/dev/null
}

verify_uploaded_asset() {
    local asset=$1 stored=$2 checksum
    checksum=$(sha256sum "$release_assets/$asset")
    if ! jq -e --arg digest "sha256:${checksum%% *}" '.state == "uploaded" and .digest == $digest' \
        <<< "$stored" >/dev/null; then
        printf 'Release asset %s has missing or different contents.\n' "$asset" >&2
        exit 1
    fi
}

# Confirm any existing assets first so a conflict cannot leave extra uploads behind.
expected_assets=(machine-whisperer.apk machine-whisperer.apk.sha256)
assets=$(list_assets)
for asset in "${expected_assets[@]}"; do
    stored=$(find_asset "$asset")
    if [[ -z "$stored" ]] || { is_owned_draft "$release" && is_empty_starter "$asset" "$stored"; }; then
        continue
    fi
    verify_uploaded_asset "$asset" "$stored"
done

for asset in "${expected_assets[@]}"; do
    stored=$(find_asset "$asset")
    if [[ -n "$stored" ]] && ! is_empty_starter "$asset" "$stored"; then
        continue
    fi
    verify_release_ref
    current_release=$(read_checked_release)
    if [[ -n "$stored" ]]; then
        asset_api="$releases_api/assets/$(jq -er '.id' <<< "$stored")"
        current_asset=$(gh api "$asset_api")
        if ! is_owned_draft "$current_release" || ! is_empty_starter "$asset" "$current_asset"; then
            printf 'The release or empty upload for %s changed during the retry.\n' "$asset" >&2
            exit 1
        fi
        gh api --method DELETE "$asset_api" --silent
    fi
    printf 'header = "Authorization: Bearer %s"\n' "$GH_TOKEN" |
        curl --disable --config - --fail --silent --show-error --output /dev/null \
            --header 'Content-Type: application/octet-stream' --data-binary "@$release_assets/$asset" \
            "$upload_url?name=$asset"
done

assets=$(list_assets)
for asset in "${expected_assets[@]}"; do
    stored=$(find_asset "$asset")
    if [[ -z "$stored" ]]; then
        printf 'Release asset %s is missing.\n' "$asset" >&2
        exit 1
    fi
    verify_uploaded_asset "$asset" "$stored"
done

release=$(read_checked_release)
verify_release_ref
if is_owned_draft "$release"; then
    gh api --method PATCH "$release_api" -F draft=false -f make_latest=legacy --silent
fi
printf 'Verified release %s with the signed APK and checksum.\n' "$GITHUB_REF_NAME"
