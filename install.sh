#!/bin/sh
# Installs the zbox CLI from its GitHub release: curl -fsSL https://box.zerops.io/install | sh
#
#   ZBOX_VERSION      release tag to install (v0.24), the latest release when unset
#   ZBOX_INSTALL_DIR  where the binary goes, /usr/local/bin when writable, else ~/.local/bin
set -eu

repo="zeropsio/zbox-cli"
base="${ZBOX_RELEASE_BASE:-https://github.com/$repo/releases}"

fail() { echo "install: $*" >&2; exit 1; }

case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	MINGW*|MSYS*|CYGWIN*) fail "on Windows run in PowerShell: irm https://box.zerops.io/install.ps1 | iex" ;;
	*) fail "unsupported system $(uname -s)" ;;
esac
case "$(uname -m)" in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) fail "unsupported architecture $(uname -m)" ;;
esac

if [ -n "${ZBOX_VERSION:-}" ]; then
	url="$base/download/$ZBOX_VERSION"
else
	url="$base/latest/download"
fi
file="zbox-$os-$arch"

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
else
	fail "curl or wget is needed"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "downloading $file${ZBOX_VERSION:+ $ZBOX_VERSION}..."
fetch "$url/$file" "$tmp/zbox" || fail "download of $url/$file failed"
fetch "$url/checksums.txt" "$tmp/checksums.txt" || fail "download of $url/checksums.txt failed"

want="$(grep " $file\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
[ -n "$want" ] || fail "$file is not in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
	have="$(sha256sum "$tmp/zbox" | cut -d' ' -f1)"
else
	have="$(shasum -a 256 "$tmp/zbox" | cut -d' ' -f1)"
fi
[ "$have" = "$want" ] || fail "checksum mismatch for $file"

if [ -n "${ZBOX_INSTALL_DIR:-}" ]; then
	dir="$ZBOX_INSTALL_DIR"
elif [ -w /usr/local/bin ]; then
	dir=/usr/local/bin
else
	dir="$HOME/.local/bin"
fi
mkdir -p "$dir"
chmod 755 "$tmp/zbox"
mv "$tmp/zbox" "$dir/zbox"

echo "installed $("$dir/zbox" --version) to $dir/zbox"
case ":$PATH:" in
	*":$dir:"*) ;;
	*) echo "add $dir to your PATH, for example: export PATH=\"$dir:\$PATH\"" ;;
esac
