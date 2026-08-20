#!/bin/sh
# evilcode install script — curl-able one-liner:
#
#   curl -fsSL https://evileko.dev/evilcode | sh
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
api="${base}/api/v1/repos/${repo}/releases/latest"

# ANSI color. Gated on stdout being a terminal so piping to a file or a
# non-interactive shell stays clean. When run as 'curl ... | sh', this
# process's stdout is still the user's terminal, so the colors show.
if [ -t 1 ]; then
  c_bold=$'\033[1m'
  c_green=$'\033[32m'
  c_cyan=$'\033[36m'
  c_dim=$'\033[2m'
  c_red=$'\033[31m'
  c_reset=$'\033[0m'
else
  c_bold= c_green= c_cyan= c_dim= c_red= c_reset=
fi

# A one-line step with a result appended after the ellipsis. Printing the
# label and the outcome on the same line keeps the output a tidy column.
say() { printf '%s%-10s%s %s' "$c_cyan" "$1" "$c_reset" "$2"; }
ok() { printf ' %s✓%s\n' "$c_green" "$c_reset"; }
note() { printf ' %s%s%s\n' "$c_dim" "$1" "$c_reset"; }

# Map the running system onto the release asset name evilcode-<os>-arch>.
case "$(uname -s)" in
  Linux) os=linux ;;
  *) printf '%serror:%s evilcode is Linux only (got %s)\n' "$c_red" "$c_reset" "$(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) printf '%serror:%s unsupported arch %s (only amd64 and arm64 are built)\n' "$c_red" "$c_reset" "$(uname -m)" >&2; exit 1 ;;
esac

asset="evilcode-${os}-${arch}"
url="${base}/${repo}/releases/download/latest/${asset}"

# Install directory: explicit arg > env var > default. ~/.local/bin is on PATH
# on most modern Linux setups, and is writable without root.
install_dir="${1:-${EVILCODE_INSTALL_DIR:-$HOME/.local/bin}}"
bin="${install_dir}/evilcode"
ec_link="${install_dir}/ec"

printf '\n%s%sevilcode installer%s\n\n' "$c_bold" "" "$c_reset"

# Retrieve the latest version tag. Best-effort: the download uses the
# /releases/download/latest/ shortcut, so a failed version lookup must never
# block the install — it just leaves the version line blank.
say 'retrieve' 'latest version … '
tag=$(curl -fsSL "$api" 2>/dev/null | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)
if [ -n "$tag" ]; then
  printf '%s%s%s\n' "$c_green" "$tag" "$c_reset"
else
  note 'unknown — continuing with latest'
  tag='latest'
fi

# Download to a temp file in the same directory, then atomically move it into
# place. A failed or partial download must never leave a half-binary at the
# install path — a broken executable there would shadow a working one elsewhere
# and the failure would be hard to see.
mkdir -p "$install_dir"
if [ ! -w "$install_dir" ]; then
  printf '%serror:%s install dir %s is not writable\n' "$c_red" "$c_reset" "$install_dir" >&2
  exit 1
fi
tmp="${install_dir}/.evilcode-install-$$"
trap 'rm -f "$tmp"' EXIT

say 'download' "evilcode ${tag} (${os}/${arch}) … "
# Show a progress bar when stderr is a real terminal (the 'curl | sh' case);
# stay silent otherwise so logs stay readable. The leading newline is only
# emitted with the progress bar, so a silent run keeps the check on the same
# line as the label.
if [ -t 2 ]; then pbar='-#'; printf '\n'; else pbar='-s'; fi
if ! curl -fL $pbar "$url" -o "$tmp"; then
  printf '%serror:%s download failed — no %s on the latest release.\n' "$c_red" "$c_reset" "$asset" >&2
  echo "  If ${arch} is not built yet, see ${base}/${repo}/releases" >&2
  exit 1
fi
chmod 0755 "$tmp"
ok

say 'install' "$bin … "
mv -f "$tmp" "$bin"
# A relative symlink survives if the install directory is moved; an absolute
# one breaks. `ec` is the short alias the README documents.
ln -sfn evilcode "$ec_link"
ok

printf '\n%sInstalled evilcode %s%s (%s)\n' "$c_bold" "$tag" "$c_reset" "$bin"
printf '  %s→%s %s (symlink to evilcode)\n' "$c_cyan" "$c_reset" "$ec_link"

# Warn only if the directory is genuinely absent from PATH — a false warning
# is noise that trains people to ignore the real one.
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *)
    printf '\n%sNote:%s %s is not on your PATH.\n' "$c_red" "$c_reset" "$install_dir" >&2
    printf '  Add it:  %sexport PATH="%s:$PATH"%s\n' "$c_dim" "$install_dir" "$c_reset" >&2
    ;;
esac

printf '\nRun %sevilcode%s to start, or %sevilcode update%s to self-update.\n\n' "$c_green" "$c_reset" "$c_green" "$c_reset"