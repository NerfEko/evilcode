#!/bin/sh
# evilcode install script — curl-able one-liner:
#
#   curl -fsSL https://git.evileko.dev/evileko/evilcode/raw/branch/main/install.sh | sh
#
# Downloads the prebuilt binary from the latest Forgejo release and installs it
# to ~/.local/bin/evilcode, plus an `ec` symlink. No Go toolchain, no clone, no
# build. Override the install directory with $EVILCODE_INSTALL_DIR or the first
# argument.
#
# The project ships a linux/amd64 binary. Other OS/arch pairs fail with a clear
# message rather than a mystery download error.
set -eu

repo="evileko/evilcode"
base="https://git.evileko.dev"

# Map the running system onto the release asset name evilcode-<os>-<arch>.
case "$(uname -s)" in
  Linux) os=linux ;;
  *) echo "evilcode: unsupported OS $(uname -s) — evilcode is Linux only." >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "evilcode: unsupported arch $(uname -m) — only amd64 and arm64 are built." >&2; exit 1 ;;
esac

asset="evilcode-${os}-${arch}"
url="${base}/${repo}/releases/download/latest/${asset}"

# Install directory: explicit arg > env var > default. ~/.local/bin is on PATH
# on most modern Linux setups, and is writable without root.
install_dir="${1:-${EVILCODE_INSTALL_DIR:-$HOME/.local/bin}}"
bin="${install_dir}/evilcode"
ec_link="${install_dir}/ec"

mkdir -p "$install_dir"
if [ ! -w "$install_dir" ]; then
  echo "evilcode: install dir ${install_dir} is not writable." >&2
  exit 1
fi

# Download to a temp file in the same directory, then atomically move it into
# place. A failed or partial download must never leave a half-binary at the
# install path — a broken executable there would shadow a working one elsewhere
# and the failure would be hard to see.
tmp="${install_dir}/.evilcode-install-$$"
trap 'rm -f "$tmp"' EXIT

echo "Downloading ${asset} from the latest release…"
if ! curl -fsSL "$url" -o "$tmp"; then
  echo "evilcode: download failed — no ${asset} on the latest release." >&2
  echo "  If ${arch} is not built yet, see https://git.evileko.dev/${repo}/releases" >&2
  exit 1
fi

chmod 0755 "$tmp"
mv -f "$tmp" "$bin"

# A relative symlink survives if the install directory is moved; an absolute
# one breaks. `ec` is the short alias the README documents.
ln -sfn evilcode "$ec_link"

echo "Installed: ${bin} (and ${ec_link} → evilcode)"

# Warn only if the directory is genuinely absent from PATH — a false warning
# is noise that trains people to ignore the real one.
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *)
    echo >&2
    echo "Note: ${install_dir} is not on your PATH." >&2
    echo "Add it:  export PATH=\"${install_dir}:\$PATH\"" >&2
    ;;
esac

echo "Run 'evilcode --help' or 'evilcode update' later to self-update."