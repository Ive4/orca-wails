#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 ]]; then
  echo "This build requires Apple Silicon macOS." >&2
  exit 1
fi

app="bin/Orca-Wails.app"
bundle_id=dev.orca-wails.desktop
if [[ "${1:-}" == --smoke-test ]]; then
  shift
  app="bin/Orca-Wails Smoke.app"
  bundle_id="dev.orca-wails.smoke.$(uuidgen | tr '[:upper:]' '[:lower:]')"
fi
version="${ORCA_WAILS_VERSION:-0.1.0}"
public_key="${ORCA_WAILS_UPDATE_PUBLIC_KEY:-}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
[[ "$public_key" =~ ^[A-Za-z0-9+/=]*$ ]]

identity="${ORCA_WAILS_SIGN_IDENTITY:-}"
signing_options=(--options runtime --timestamp)
local_signing=0
local_identity="$HOME/Library/Application Support/Orca-Wails/signing-identity"
if [[ -z "$identity" && "$bundle_id" == dev.orca-wails.desktop && -f "$local_identity" ]]; then
  identity="$(cat "$local_identity")"
  if [[ ! "$identity" =~ ^[0-9A-Fa-f]{40}$ ]]; then
    printf 'Invalid code signing certificate fingerprint in %s\n' "$local_identity" >&2
    exit 1
  fi
  signing_options=(--timestamp=none)
  local_signing=1
fi

seed=
case "${1:-}" in
  --archive)
    seed="${2:?missing archive path}"
    archive_identity="${seed%.*}.json"
    [[ -f "$seed" && -f "$archive_identity" ]]
    ;;
  --no-core)
    ;;
  *)
    core_dir=dist-core/macos-arm64-bun
    ORCA_WAILS_SIGN_IDENTITY="${identity:--}" ORCA_WAILS_LOCAL_SIGNING="$local_signing" \
      python3 scripts/extract-macos-core.py "$@" --output "$core_dir"
    seed="$core_dir/bootstrap-core.zip"
    archive_identity="$core_dir/bootstrap-core.json"
    ;;
esac

mkdir -p bin
stage="$(mktemp -d bin/.macos-build.XXXXXX)"
trap 'rm -rf "$stage"' EXIT
bundle="$stage/${app##*/}"
mkdir -p "$bundle/Contents/MacOS" "$bundle/Contents/Resources"
cp build/Info.plist "$bundle/Contents/"
cp build/icon.icns "$bundle/Contents/Resources/"
cp -R native/macos/TaskSounds "$bundle/Contents/Resources/"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $version" "$bundle/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $version" "$bundle/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier $bundle_id" "$bundle/Contents/Info.plist"
if [[ -n "$seed" ]]; then
  cp "$seed" "$bundle/Contents/Resources/bootstrap-core.zip"
  cp "$archive_identity" "$bundle/Contents/Resources/bootstrap-core.json"
fi

export MACOSX_DEPLOYMENT_TARGET=13.5
export CGO_CFLAGS="${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=13.5"
swift_archive="$(pwd)/$stage/libOrcaTray.a"
xcrun swiftc -emit-library -static -O -whole-module-optimization -module-name OrcaTray \
  -swift-version 6 -target arm64-apple-macosx13.5 -sdk "$(xcrun --sdk macosx --show-sdk-path)" \
  native/macos/*.swift -o "$swift_archive"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} $swift_archive"
CGO_ENABLED=1 go build -tags production,swifttray -trimpath -ldflags="-s -w -extldflags '-mmacosx-version-min=13.5 -Wl,-rpath,/usr/lib/swift' -X main.version=$version -X main.instanceID=$bundle_id -X main.appUpdatePublicKey=$public_key" \
  -o "$bundle/Contents/MacOS/orca-wails" .
if [[ "${identity:--}" == - ]]; then
  codesign --force --sign - "$bundle"
else
  codesign --force --sign "$identity" "${signing_options[@]}" "$bundle"
fi
codesign --verify --strict "$bundle"
rm -rf "$app"
mv "$bundle" "$app"
printf '%s\n' "$app"
