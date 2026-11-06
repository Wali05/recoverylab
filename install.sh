#!/bin/sh
# Installs RecoveryLab from GitHub Releases after checking its SHA-256 sum.
#
#   curl -fsSL https://raw.githubusercontent.com/Wali05/recoverylab/main/install.sh | sh
#
# RECOVERYLAB_VERSION=v0.6.0 picks a release (default: the latest).
# RECOVERYLAB_INSTALL_DIR picks the folder (default: ~/.local/bin).
set -eu

repo="Wali05/recoverylab"
github="${RECOVERYLAB_GITHUB:-https://github.com}"
api="${RECOVERYLAB_API:-https://api.github.com}"
dir="${RECOVERYLAB_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "recoverylab install: $*" >&2
	exit 1
}
command -v curl >/dev/null 2>&1 || fail "curl is required"

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "no installer for $(uname -s); download a binary from $github/$repo/releases" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "no release binary for $(uname -m); try: go install github.com/$repo/cmd/recoverylab@latest" ;;
esac

version="${RECOVERYLAB_VERSION:-}"
if [ -z "$version" ]; then
	# A token, when CI has one, avoids the API's low anonymous rate limit.
	token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
	if [ -n "$token" ]; then
		latest=$(curl -fsSL -H "Authorization: Bearer $token" "$api/repos/$repo/releases/latest") || fail "couldn't reach $api"
	else
		latest=$(curl -fsSL "$api/repos/$repo/releases/latest") || fail "couldn't reach $api"
	fi
	version=$(printf '%s\n' "$latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$version" ] || fail "couldn't find the latest release"
fi
case "$version" in
v*) ;;
*) version="v$version" ;;
esac

asset="recoverylab_${version#v}_${os}_${arch}"
base="$github/$repo/releases/download/$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading RecoveryLab $version for $os/$arch"
curl -fsSL -o "$tmp/$asset" "$base/$asset" || fail "couldn't download $base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "couldn't download checksums.txt"

want=$(awk -v name="$asset" '$2 == name { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "checksums.txt doesn't list $asset"
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
fi
[ "$got" = "$want" ] || fail "$asset doesn't match its published checksum; nothing was installed"

mkdir -p "$dir"
chmod +x "$tmp/$asset"
mv "$tmp/$asset" "$dir/recoverylab"
echo "Installed $("$dir/recoverylab" version) to $dir/recoverylab (checksum verified)"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "Add $dir to your PATH to run it as recoverylab." ;;
esac
