#!/bin/sh
# Build the macOS app: a universal acp.app (Swift shell + bundled acp + Sparkle) and its DMG in
# dist/desktop/, with checksums. Signing, notarization and the appcast run only when their
# secrets are set; without them the app is signed ad hoc, which runs on this machine only.
#   scripts/build-desktop.sh v0.1.0-beta.1
#
#   ACP_DEVELOPER_ID              codesign identity ("Developer ID Application: ..."), in a keychain
#   ACP_NOTARY_KEY_FILE, ACP_NOTARY_KEY_ID, ACP_NOTARY_ISSUER   App Store Connect API key (notarytool)
#   ACP_SPARKLE_FEED_URL, ACP_SPARKLE_PUBLIC_KEY                https appcast URL + EdDSA public key
#   ACP_SPARKLE_PRIVATE_KEY_FILE  EdDSA private key: writes dist/desktop/appcast/appcast.xml
#   ACP_SPARKLE_DOWNLOAD_URL_PREFIX  https folder the DMG is served from, required with the private
#                                    key: the appcast's download link must open without a GitHub login
set -eu

tag=${1:?usage: scripts/build-desktop.sh TAG}
case $tag in
  v[0-9]*) ;;
  *) echo "build-desktop: tag must look like v1.2.3 or v1.2.3-beta.1, got $tag" >&2; exit 2 ;;
esac
version=${tag#v}
root=$(cd "$(dirname "$0")/.." && pwd)
# Sparkle compares CFBundleVersion and drops a "-beta.1" suffix, so it gets a numeric form.
build=$(sh "$root/scripts/bundle-version.sh" "$tag")
out=$root/dist/desktop
fail() { echo "build-desktop: $*" >&2; exit 1; }

[ "$(uname -s)" = Darwin ] || fail "the app builds on macOS only"
# Use the go that owns GOROOT when a version manager left another go first on PATH.
. "$root/scripts/lib/use-goroot-go.sh"
use_goroot_go build-desktop
feed=${ACP_SPARKLE_FEED_URL:-}
pubkey=${ACP_SPARKLE_PUBLIC_KEY:-}
if { [ -n "$feed" ] && [ -z "$pubkey" ]; } || { [ -z "$feed" ] && [ -n "$pubkey" ]; }; then
  fail "set both ACP_SPARKLE_FEED_URL and ACP_SPARKLE_PUBLIC_KEY, or neither (Sparkle stays off)"
fi
case $feed in ''|https://?*) ;; *) fail "ACP_SPARKLE_FEED_URL must be https, got $feed" ;; esac
identity=${ACP_DEVELOPER_ID:-}
notary=
if [ -n "${ACP_NOTARY_KEY_FILE:-}" ] && [ -n "${ACP_NOTARY_KEY_ID:-}" ] && [ -n "${ACP_NOTARY_ISSUER:-}" ]; then
  [ -n "$identity" ] || fail "notarization needs ACP_DEVELOPER_ID"
  notary=1
fi
prefix=${ACP_SPARKLE_DOWNLOAD_URL_PREFIX:-}
if [ -n "${ACP_SPARKLE_PRIVATE_KEY_FILE:-}" ]; then
  [ -n "$feed" ] || fail "ACP_SPARKLE_PRIVATE_KEY_FILE is set but the app has no feed (ACP_SPARKLE_FEED_URL)"
  case $prefix in
    https://?*) ;;
    '') fail "ACP_SPARKLE_PRIVATE_KEY_FILE is set but ACP_SPARKLE_DOWNLOAD_URL_PREFIX is empty; Sparkle needs a public https folder for the DMG" ;;
    *) fail "ACP_SPARKLE_DOWNLOAD_URL_PREFIX must be https, got $prefix" ;;
  esac
  # generate_appcast resolves file names against it, so without the slash it drops the last segment.
  prefix=${prefix%/}/
fi

# Only dist/desktop is ours: the CLI archives in dist/ stay.
rm -rf "$out"
mkdir -p "$out/work"
work=$out/work

# 1. acp, universal.
for arch in amd64 arm64; do
  (cd "$root" && CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X github.com/thieung/acp/internal/commands.Version=$tag" \
    -o "$work/acp-$arch" ./cmd/acp)
done
lipo -create -output "$work/acp" "$work/acp-amd64" "$work/acp-arm64"

# 2. The Swift shell, universal, with Sparkle.
swift build -c release --arch arm64 --arch x86_64 --package-path "$root/desktop"
products=$root/desktop/.build/apple/Products/Release

# 3. The bundle.
app=$out/acp.app
contents=$app/Contents
mkdir -p "$contents/MacOS" "$contents/Helpers" "$contents/Frameworks"
cp "$products/AcpDesktop" "$contents/MacOS/AcpDesktop"
cp "$work/acp" "$contents/Helpers/acp"
ditto "$products/Sparkle.framework" "$contents/Frameworks/Sparkle.framework"
plist=$contents/Info.plist
cp "$root/desktop/Resources/Info.plist" "$plist"
plutil -replace CFBundleShortVersionString -string "$version" "$plist"
plutil -replace CFBundleVersion -string "$build" "$plist"
plutil -replace SUFeedURL -string "$feed" "$plist"
plutil -replace SUPublicEDKey -string "$pubkey" "$plist"
plutil -lint "$plist" >/dev/null
if grep -q '@[A-Z_]*@' "$plist"; then fail "Info.plist still has a placeholder"; fi
printf 'APPL????' >"$contents/PkgInfo"

# 4. SwiftPM links Sparkle by @rpath but only adds ../lib; the app keeps it in ../Frameworks.
install_name_tool -add_rpath @executable_path/../Frameworks "$contents/MacOS/AcpDesktop"

# 5. Sign inside out: nested code first, the app last.
if [ -n "$identity" ]; then
  set -- --force --options runtime --timestamp -s "$identity"
else
  set -- --force -s -
fi
sparkle=$contents/Frameworks/Sparkle.framework/Versions/B
codesign "$@" "$contents/Helpers/acp"
for xpc in "$sparkle"/XPCServices/*.xpc; do
  codesign "$@" --preserve-metadata=entitlements "$xpc"
done
codesign "$@" "$sparkle/Autoupdate"
codesign "$@" "$sparkle/Updater.app"
codesign "$@" "$contents/Frameworks/Sparkle.framework"
codesign "$@" "$app"
codesign --verify --deep --strict "$app"

# 6. The DMG: the app next to a link to /Applications.
dmg=$out/acp-$version.dmg
stage=$work/dmg
mkdir -p "$stage"
ditto "$app" "$stage/acp.app"
ln -s /Applications "$stage/Applications"
hdiutil create -quiet -volname acp -srcfolder "$stage" -format UDZO -ov "$dmg"
[ -z "$identity" ] || codesign --force --timestamp -s "$identity" "$dmg"

# 7. Notarize the DMG (which covers the app in it) and staple both.
if [ -n "$notary" ]; then
  xcrun notarytool submit "$dmg" --key "$ACP_NOTARY_KEY_FILE" --key-id "$ACP_NOTARY_KEY_ID" \
    --issuer "$ACP_NOTARY_ISSUER" --wait
  xcrun stapler staple "$dmg"
  xcrun stapler staple "$app"
  spctl -a -t exec -vv "$app"
fi

# 8. The appcast for this release, signed with the EdDSA key.
if [ -n "${ACP_SPARKLE_PRIVATE_KEY_FILE:-}" ]; then
  mkdir -p "$out/appcast"
  cp "$dmg" "$out/appcast/"
  "$root/desktop/.build/artifacts/sparkle/Sparkle/bin/generate_appcast" \
    --ed-key-file "$ACP_SPARKLE_PRIVATE_KEY_FILE" --download-url-prefix "$prefix" "$out/appcast"
  rm "$out/appcast/acp-$version.dmg"
fi

rm -rf "$work"
cd "$out"
shasum -a 256 "acp-$version.dmg" >>checksums.txt
cat checksums.txt
