#!/bin/sh
# evilcode install / update / uninstall script — curl-able one-liner:
#
#   curl -fsSL https://evileko.dev/evilcode | sh
#
# Fresh: downloads the prebuilt binary from the latest Forgejo release and
# installs it to ~/.local/bin/evilcode, plus an `ec` symlink. No Go toolchain,
# no clone, no build.
#
# If evilcode is already installed, it asks what to do instead of silently
# reinstalling: check for updates, reinstall, remove, or reset config.
#
# Override the install directory with $EVILCODE_INSTALL_DIR or the first
# argument. The project ships a linux/amd64 binary; other OS/arch pairs fail
# with a clear message rather than a mystery download error.
set -eu

repo="evileko/evilcode"
base="https://git.evileko.dev"
api="${base}/api/v1/repos/${repo}/releases/latest"

# ANSI color. Gated on stdout being a terminal so piping to a file or a
# non-interactive shell stays clean. When run as 'curl ... | sh', this
# process's stdout is still the user's terminal, so the colors show.
interactive=0
if [ -t 1 ]; then
  interactive=1
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

# Prompt for a y/n answer. Reads from /dev/tty so it works under 'curl | sh',
# where this script's own stdin is the pipe delivering the script. The prompt
# is written to stdout; the answer comes from the controlling terminal. An
# empty answer (just Enter) takes the default passed as $2.
CONFIRM_ANS=
confirm() {
  printf '%s' "$1"
  CONFIRM_ANS=
  read -r CONFIRM_ANS < /dev/tty 2>/dev/null || CONFIRM_ANS=
  # if/then (not '[ -z ] && assign'): under 'set -e' a short-circuited && list
  # returns non-zero and aborts the script, so use an explicit test.
  if [ -z "$CONFIRM_ANS" ]; then CONFIRM_ANS="$2"; fi
  case "$CONFIRM_ANS" in
    y|Y|yes|Yes|YES) return 0 ;;
    *) return 1 ;;
  esac
}

# Prompt for one of several choices; $1 is the prompt, $2 the default.
MENU_PICK=
menu_pick() {
  printf '%s' "$1"
  MENU_PICK=
  read -r MENU_PICK < /dev/tty 2>/dev/null || MENU_PICK=
  if [ -z "$MENU_PICK" ]; then MENU_PICK="$2"; fi
}

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
config_dir="${EVILCODE_CONFIG_DIR:-$HOME/.config/evilcode}"
data_dir="${EVILCODE_DATA_DIR:-$HOME/.local/share/evilcode}"

# Detect an existing install. Check the install directory first (where this
# script puts the binary), then PATH. The raw path is kept as-is so removal
# deletes the right thing whether it is a real file or a symlink.
installed_path=
detect_installed() {
  for c in "$bin" "$(command -v evilcode 2>/dev/null)"; do
    if [ -n "$c" ] && [ -e "$c" ]; then
      installed_path="$c"
      return 0
    fi
  done
  return 1
}

# Download + atomically install the latest binary, then print tips. Shared by
# the fresh-install and reinstall paths.
do_install() {
  printf '\n%s%sevilcode installer%s\n\n' "$c_bold" "" "$c_reset"

  # Retrieve the latest version tag. Best-effort: the download uses the
  # /releases/download/latest/ shortcut, so a failed lookup never blocks the
  # install — it just leaves the version line blank.
  say 'retrieve' 'latest version … '
  tag=$(curl -fsSL "$api" 2>/dev/null | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)
  if [ -n "$tag" ]; then
    printf '%s%s%s\n' "$c_green" "$tag" "$c_reset"
  else
    note 'unknown — continuing with latest'
    tag='latest'
  fi

  # Download to a temp file in the same directory, then atomically move it
  # into place. A failed or partial download must never leave a half-binary at
  # the install path — a broken executable there would shadow a working one
  # elsewhere and the failure would be hard to see.
  mkdir -p "$install_dir"
  if [ ! -w "$install_dir" ]; then
    printf '%serror:%s install dir %s is not writable\n' "$c_red" "$c_reset" "$install_dir" >&2
    exit 1
  fi
  tmp="${install_dir}/.evilcode-install-$$"
  trap 'rm -f "$tmp"' EXIT

  say 'download' "evilcode ${tag} (${os}/${arch}) … "
  # Progress bar when stderr is a terminal (the 'curl | sh' case); silent
  # otherwise. The leading newline only ships with the bar, so a silent run
  # keeps the check on the label's line.
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

  print_tips
}

print_tips() {
  printf '\n%sNext steps%s\n' "$c_bold" "$c_reset"
  printf '  %s$%s evilcode            launch the TUI (or %sec%s)\n' "$c_dim" "$c_reset" "$c_green" "$c_reset"
  printf '  %s$%s ec -m <model>@<provider>  start with a specific model\n' "$c_dim" "$c_reset"
  printf '  %s$%s evilcode run "prompt"    headless one-shot\n' "$c_dim" "$c_reset"

  printf '\n%sTips%s\n' "$c_bold" "$c_reset"
  printf '  - Run %sollama serve%s for a local Ollama endpoint. Without an\n' "$c_green" "$c_reset"
  printf '    Ollama Cloud key, models route through the local daemon.\n'
  printf '  - Add a provider key with %s/login%s in the TUI, or set %s$OLLAMA_API_KEY%s\n' "$c_cyan" "$c_reset" "$c_dim" "$c_reset"
  printf '    (or %s$OPENAI_API_KEY%s / %s$DEEPSEEK_API_KEY%s); %s/model%s switches models.\n' "$c_dim" "$c_reset" "$c_dim" "$c_reset" "$c_cyan" "$c_reset"
  printf '  - For the full toolset install %srg%s (grep), %stmux%s (probe), %sgopls%s (lsp).\n' "$c_green" "$c_reset" "$c_green" "$c_reset" "$c_green" "$c_reset"
  printf '  - Self-update with %sevilcode update%s; shell completions with\n' "$c_cyan" "$c_reset"
  printf '    %sevilcode completions bash|zsh|fish%s.\n' "$c_cyan" "$c_reset"
  printf '\n'
}

# Hand off to the installed binary's own updater, which compares versions and
# either reports 'already up to date' or downloads and swaps in the latest.
do_check_updates() {
  printf '\n%sChecking for updates…%s\n' "$c_bold" "$c_reset"
  "$installed_path" update
}

# Remove the binary and the ec symlink. Optionally also remove config and
# session data, with a separate confirmation so no one loses their setup by
# accident.
do_remove() {
  ec_here="$(dirname "$installed_path")/ec"
  if ! confirm "Remove evilcode (delete $installed_path and $ec_here)? [y/N] " n; then
    printf 'Not removed.\n'
    return 0
  fi
  rm -f "$installed_path" "$ec_here"
  printf '%s✓%s removed %s' "$c_green" "$c_reset" "$installed_path"
  printf ' and %s' "$ec_here"
  printf '\n'
  if confirm "Also remove config and data ($config_dir, $data_dir)? [y/N] " n; then
    rm -rf "$config_dir" "$data_dir"
    printf '%s✓%s removed config and data\n' "$c_green" "$c_reset"
  fi
  printf '%sUninstalled evilcode.%s\n' "$c_green" "$c_reset"
}

# Reset config by backing it up then removing it, so defaults apply on next
# launch without losing the old settings irreversibly.
do_reset_config() {
  if [ ! -e "$config_dir" ]; then
    printf 'No config at %s — nothing to reset.\n' "$config_dir"
    return 0
  fi
  if ! confirm "Reset config? Back up $config_dir and remove it so defaults apply. [y/N] " n; then
    printf 'Not reset.\n'
    return 0
  fi
  bak="${config_dir}.bak.$(date +%Y%m%d-%H%M%S)"
  mv "$config_dir" "$bak"
  printf '%s✓%s backed up to %s\n' "$c_green" "$c_reset" "$bak"
  printf 'Defaults will apply on next launch.\n'
}

# --- main --------------------------------------------------------------------

if detect_installed; then
  printf '\n%sevilcode is already installed%s at %s%s%s\n' "$c_bold" "$c_reset" "$c_cyan" "$installed_path" "$c_reset"
  if [ "$interactive" -eq 0 ]; then
    # No terminal to ask in: do nothing destructive without consent. The
    # user can re-run in a terminal, or use 'evilcode update' directly.
    printf 'Re-run in a terminal to check for updates, reinstall, remove, or reset config.\n'
    exit 0
  fi
  printf '\nWhat would you like to do?\n'
  printf '  %s1)%s Check for updates\n' "$c_cyan" "$c_reset"
  printf '  %s2)%s Reinstall latest\n' "$c_cyan" "$c_reset"
  printf '  %s3)%s Remove evilcode\n' "$c_cyan" "$c_reset"
  printf '  %s4)%s Reset config\n' "$c_cyan" "$c_reset"
  printf '  %s5)%s Exit\n' "$c_cyan" "$c_reset"
  menu_pick 'Choose [1]: ' 1
  case "$MENU_PICK" in
    1) do_check_updates; exit 0 ;;
    2) do_install; exit 0 ;;
    3) do_remove; exit 0 ;;
    4) do_reset_config; exit 0 ;;
    *) printf 'Exiting.\n'; exit 0 ;;
  esac
else
  do_install
fi