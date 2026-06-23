#!/usr/bin/env sh
# remolo installer.
#
# Downloads the latest released remolo binary for your OS/arch, installs it to
# ~/.local/bin, makes sure that directory is on your PATH (bash/zsh/profile),
# and on Linux installs and starts a systemd --user service for `remolo host`.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/mirkobrombin/remolo/master/install.sh | sh
# or, from a checkout:
#   ./install.sh
#
# Environment overrides:
#   REMOLO_VERSION=nightly   install the rolling nightly instead of latest stable
#   REMOLO_NO_SERVICE=1      do not install/enable the systemd user service
set -eu

REPO="mirkobrombin/remolo"
BIN_DIR="${HOME}/.local/bin"
VERSION="${REMOLO_VERSION:-latest}"

info() { printf 'remolo-install: %s\n' "$1"; }
err()  { printf 'remolo-install: error: %s\n' "$1" >&2; exit 1; }

# --- detect OS and architecture -------------------------------------------
os=$(uname -s 2>/dev/null || echo unknown)
case "$os" in
  Linux)  OS=linux ;;
  Darwin) OS=darwin ;;
  *) err "unsupported OS: $os (Windows: download the .exe from the releases page)" ;;
esac

machine=$(uname -m 2>/dev/null || echo unknown)
case "$machine" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) err "unsupported architecture: $machine" ;;
esac

ASSET="remolo-${OS}-${ARCH}"
if [ "$VERSION" = "latest" ]; then
  URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
else
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
fi

# --- download the binary ---------------------------------------------------
mkdir -p "$BIN_DIR"
TARGET="${BIN_DIR}/remolo"
info "downloading ${ASSET} (${VERSION}) -> ${TARGET}"
if command -v curl >/dev/null 2>&1; then
  curl -fSL "$URL" -o "$TARGET" || err "download failed from $URL"
elif command -v wget >/dev/null 2>&1; then
  wget -O "$TARGET" "$URL" || err "download failed from $URL"
else
  err "need curl or wget to download"
fi
chmod +x "$TARGET"
info "installed $("$TARGET" --version 2>/dev/null || echo remolo)"

# --- ensure ~/.local/bin is on PATH ---------------------------------------
PATH_LINE='export PATH="$HOME/.local/bin:$PATH"'
MARKER='# added by remolo-install'
add_path() {
  rc="$1"
  [ -e "$rc" ] || return 0
  if ! grep -qF "$MARKER" "$rc" 2>/dev/null; then
    printf '\n%s\n%s\n' "$MARKER" "$PATH_LINE" >> "$rc"
    info "added ~/.local/bin to PATH in $rc"
  fi
}
# Touch the common rc files so the PATH update sticks for both shells.
[ -f "${HOME}/.bashrc" ] || [ "${SHELL##*/}" = bash ] && touch "${HOME}/.bashrc" 2>/dev/null || true
[ -f "${HOME}/.zshrc" ]  || [ "${SHELL##*/}" = zsh ]  && touch "${HOME}/.zshrc" 2>/dev/null || true
add_path "${HOME}/.bashrc"
add_path "${HOME}/.zshrc"
add_path "${HOME}/.profile"

# --- systemd --user service (Linux only) ----------------------------------
if [ "$OS" = linux ] && [ "${REMOLO_NO_SERVICE:-0}" != 1 ] && command -v systemctl >/dev/null 2>&1; then
  UNIT_DIR="${HOME}/.config/systemd/user"
  ENV_DIR="${HOME}/.config/remolo"
  mkdir -p "$UNIT_DIR" "$ENV_DIR"

  if [ ! -f "${ENV_DIR}/host.env" ]; then
    printf 'REMOLO_HOST_ARGS=--mdns\n' > "${ENV_DIR}/host.env"
    info "wrote default ${ENV_DIR}/host.env (edit to change host flags)"
  fi

  cat > "${UNIT_DIR}/remolo-host.service" <<EOF
[Unit]
Description=remolo host (remote control endpoint)
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=-%h/.config/remolo/host.env
ExecStart=%h/.local/bin/remolo host \$REMOLO_HOST_ARGS
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
EOF

  if systemctl --user daemon-reload 2>/dev/null; then
    systemctl --user enable --now remolo-host.service 2>/dev/null \
      && info "remolo-host.service enabled and started (user)" \
      || info "could not auto-start the service (no user session bus?); start it later with: systemctl --user enable --now remolo-host.service"
    info "read your session token with: journalctl --user -u remolo-host -f"
    info "tip: 'loginctl enable-linger $USER' keeps the host running after logout"
  else
    info "systemd --user not available in this session; skipped service setup"
  fi
else
  info "skipping systemd service (not Linux, disabled, or systemctl missing)"
fi

info "done. Open a new shell (or 'source ~/.bashrc') so 'remolo' is on your PATH."
