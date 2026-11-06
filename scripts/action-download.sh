#!/usr/bin/env bash
# Used by action.yml. Fetches the release binary for this runner and checks
# it against checksums.txt, or tells the next steps to build from source.
set -uo pipefail

build() {
  echo "$1; building from source."
  echo "build=true" >> "$GITHUB_OUTPUT"
  exit 0
}

ref="${ACTION_REF:-}"
[[ "$ref" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || build "Using ${ref:-a local checkout}, not a release"
case "${RUNNER_OS:-}" in
  Linux) os=linux ;; macOS) os=darwin ;; Windows) os=windows ;; *) build "No release binary for ${RUNNER_OS:-this OS}" ;;
esac
case "${RUNNER_ARCH:-}" in
  X64) arch=amd64 ;; ARM64) arch=arm64 ;; *) build "No release binary for ${RUNNER_ARCH:-this CPU}" ;;
esac
ext=""
[[ "$os" == windows ]] && ext=".exe"
asset="recoverylab_${ref#v}_${os}_${arch}${ext}"
base="${RELEASES:-https://github.com}/${ACTION_REPO:-Wali05/recoverylab}/releases/download/$ref"
dir="$RUNNER_TEMP/recoverylab-bin"
mkdir -p "$dir"
cd "$dir" || build "Can't use $dir"

curl -fsSL --retry 3 -o "$asset" "$base/$asset" || build "Couldn't download $asset"
curl -fsSL --retry 3 -o checksums.txt "$base/checksums.txt" || build "Couldn't download checksums.txt"
line=$(grep -E "  $asset\$" checksums.txt) || build "checksums.txt doesn't list $asset"
if command -v sha256sum > /dev/null; then
  echo "$line" | sha256sum -c --quiet - || { echo "::error::$asset doesn't match its published checksum"; exit 1; }
else
  echo "$line" | shasum -a 256 -c --quiet - || { echo "::error::$asset doesn't match its published checksum"; exit 1; }
fi
mv "$asset" "recoverylab$ext"
chmod +x "recoverylab$ext"
rm -f checksums.txt
echo "Downloaded RecoveryLab $ref ($os/$arch) and verified its checksum."
echo "build=false" >> "$GITHUB_OUTPUT"
