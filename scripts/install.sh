#!/bin/sh
# Install Geko on Linux or macOS from its GitHub release:
#
#   curl -fsSL https://raw.githubusercontent.com/YunusEmreAlps/geko/master/scripts/install.sh | sh
#
# GEKO_VERSION picks a release (default: the latest) and GEKO_INSTALL_DIR the
# directory (default: ~/.local/bin). The archive is checked against the
# release's checksums.txt before anything is installed.
set -eu

repo="YunusEmreAlps/geko"
dir="${GEKO_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "geko install: $*" >&2
	exit 1
}

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "unsupported system $(uname -s); download an archive from https://github.com/$repo/releases" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported processor $(uname -m); download an archive from https://github.com/$repo/releases" ;;
esac

version="${GEKO_VERSION:-}"
if [ -z "$version" ]; then
	version=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$version" ] || fail "could not find the latest release"
fi
version=${version#v}

archive="geko_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/v$version"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

echo "Downloading $archive"
curl -fsSL -o "$work/$archive" "$base/$archive" || fail "could not download $base/$archive"
curl -fsSL -o "$work/checksums.txt" "$base/checksums.txt" || fail "could not download $base/checksums.txt"
expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$work/checksums.txt")
[ -n "$expected" ] || fail "checksums.txt does not list $archive"
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$work/$archive" | awk '{ print $1 }')
else
	actual=$(shasum -a 256 "$work/$archive" | awk '{ print $1 }')
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $archive"

tar -xzf "$work/$archive" -C "$work" geko
mkdir -p "$dir"
cp "$work/geko" "$dir/geko"
chmod 0755 "$dir/geko"
echo "Installed Geko $version to $dir/geko"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "Add $dir to your PATH to run geko from any directory." ;;
esac
echo "Try it with: geko demo"
