#!/usr/bin/env bash
set -euo pipefail

# HomeLab Dashboard bootstrap installer.
#
# The installer intentionally runs as the unprivileged account that owns the
# rootless Podman socket. sudo is used only for installing missing host
# packages. The downloaded release bundle contains the compose configuration
# and source needed by deploy/host-agent/install.sh; no Go or Node.js toolchain
# is required on the host.

readonly PROJECT="homelab-dashboard"
readonly DEFAULT_REPOSITORY="bnhminh1010/homelab-dashboard"
: "${HOME:?HOME is required}"
readonly DEFAULT_INSTALL_DIR="${XDG_DATA_HOME:-$HOME}/$PROJECT"

repository="${HOMELAB_REPOSITORY:-$DEFAULT_REPOSITORY}"
release_tag="${RELEASE_TAG:-}"
install_dir="${HOMELAB_INSTALL_DIR:-$DEFAULT_INSTALL_DIR}"
dry_run=0
non_interactive=0
skip_host_agent=0
tmp_dir=""

usage() {
  cat <<'EOF'
HomeLab Dashboard installer

Usage: install.sh [options]

Options:
  --version TAG       Install an exact release tag (for example v1.2.3).
  --install-dir PATH  Use PATH instead of ~/.local/share/homelab-dashboard.
  --repository OWNER/REPO
                      Download releases from another GitHub repository.
  --non-interactive   Fail if TS_AUTHKEY or ADMIN_USERS are not provided.
  --skip-host-agent   Do not install/restart the host-agent user service.
  --dry-run           Validate inputs and print actions without changing host state.
  -h, --help          Show this help.

Environment:
  TS_AUTHKEY, ADMIN_USERS, DASHBOARD_IMAGE, XDG_RUNTIME_DIR
  RELEASE_TAG, HOMELAB_INSTALL_DIR, HOMELAB_REPOSITORY
EOF
}

die() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

info() {
  printf '%s\n' "$*"
}

warn() {
  printf 'Warning: %s\n' "$*" >&2
}

cleanup() {
  if [[ -n "$tmp_dir" && -d "$tmp_dir" ]]; then
    rm -rf "$tmp_dir"
  fi
}
trap cleanup EXIT

while (($# > 0)); do
  case "$1" in
    --version)
      (($# >= 2)) || die "--version requires a tag"
      release_tag="$2"
      shift 2
      ;;
    --install-dir)
      (($# >= 2)) || die "--install-dir requires a path"
      install_dir="$2"
      shift 2
      ;;
    --repository)
      (($# >= 2)) || die "--repository requires OWNER/REPO"
      repository="$2"
      shift 2
      ;;
    --non-interactive)
      non_interactive=1
      shift
      ;;
    --skip-host-agent)
      skip_host_agent=1
      shift
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown option: $1 (use --help for usage)"
      ;;
  esac
done

((EUID != 0)) || die "run as the rootless Podman account, not root"
[[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "repository must be OWNER/REPO"
[[ "$install_dir" = /* ]] || die "--install-dir must be an absolute path"

if [[ -n "$release_tag" ]]; then
  [[ "$release_tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || \
    die "release tag must look like v1.2.3 or v1.2.3-beta.1"
fi

runtime_dir="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
if [[ -z "${TS_AUTHKEY:-}" && $non_interactive -eq 0 && $dry_run -eq 0 ]]; then
  [[ -r /dev/tty ]] || die "TS_AUTHKEY is required when no interactive terminal is available"
  read -r -s -p "Tailscale auth key: " TS_AUTHKEY </dev/tty
  printf '\n'
  export TS_AUTHKEY
fi
if [[ -z "${ADMIN_USERS:-}" && $non_interactive -eq 0 && $dry_run -eq 0 ]]; then
  [[ -r /dev/tty ]] || die "ADMIN_USERS is required when no interactive terminal is available"
  read -r -p "Admin login names (comma-separated): " ADMIN_USERS </dev/tty
  export ADMIN_USERS
fi
[[ -n "${TS_AUTHKEY:-}" || $dry_run -eq 1 ]] || die "TS_AUTHKEY must not be empty"
[[ -n "${ADMIN_USERS:-}" || $dry_run -eq 1 ]] || die "ADMIN_USERS must not be empty"

os_id="unknown"
os_like=""
if [[ -r /etc/os-release ]]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  os_id="${ID:-unknown}"
  os_like="${ID_LIKE:-}"
fi

package_manager=""
case " $os_id $os_like " in
  *debian*|*ubuntu*|*apt*) package_manager="apt" ;;
  *fedora*|*rhel*|*centos*|*rocky*|*alma*|*dnf*) package_manager="dnf" ;;
  *arch*|*manjaro*|*pacman*) package_manager="pacman" ;;
esac

required_commands=(curl tar sha256sum systemctl podman)
missing_commands=()
for command_name in "${required_commands[@]}"; do
  command -v "$command_name" >/dev/null 2>&1 || missing_commands+=("$command_name")
done
if command -v podman >/dev/null 2>&1; then
  if ! podman compose version >/dev/null 2>&1 && ! command -v podman-compose >/dev/null 2>&1; then
    missing_commands+=(podman-compose)
  fi
fi

install_packages() {
  local packages=(curl ca-certificates tar coreutils systemd podman podman-compose)
  case "$package_manager" in
    apt)
      command -v sudo >/dev/null 2>&1 || die "sudo is required to install missing APT packages"
      sudo apt-get update
      sudo apt-get install -y "${packages[@]}"
      ;;
    dnf)
      command -v sudo >/dev/null 2>&1 || die "sudo is required to install missing DNF packages"
      sudo dnf install -y "${packages[@]}"
      ;;
    pacman)
      command -v sudo >/dev/null 2>&1 || die "sudo is required to install missing Pacman packages"
      sudo pacman -Sy --needed --noconfirm curl ca-certificates tar coreutils systemd podman podman-compose
      ;;
    *)
      die "unsupported Linux distribution ($os_id); install ${missing_commands[*]} manually"
      ;;
  esac
}

if ((${#missing_commands[@]} > 0)); then
  if ((dry_run)); then
    warn "missing commands: ${missing_commands[*]}"
    [[ -n "$package_manager" ]] && info "Would install dependencies with $package_manager"
  else
    info "Installing missing host dependencies with $package_manager..."
    install_packages
  fi
fi

if ((dry_run)); then
  info "Dry run: release=${release_tag:-latest stable} repository=$repository"
  info "Dry run: install directory=$install_dir"
  info "Dry run: runtime directory=$runtime_dir"
  info "Dry run: would download, verify, extract, configure .env, install host-agent, and start Compose"
  exit 0
fi

for command_name in "${required_commands[@]}"; do
  command -v "$command_name" >/dev/null 2>&1 || die "required command not found after dependency setup: $command_name"
done

compose_cmd=(podman compose)
if ! podman compose version >/dev/null 2>&1; then
  command -v podman-compose >/dev/null 2>&1 || die "podman Compose provider is not available"
  compose_cmd=(podman-compose)
fi

resolve_latest_tag() {
  local response
  response="$(curl -fsSL --proto '=https' --tlsv1.2 \
    -H 'Accept: application/vnd.github+json' \
    -H 'User-Agent: homelab-dashboard-installer' \
    "https://api.github.com/repos/$repository/releases/latest")" || \
    die "unable to resolve the latest stable release for $repository"
  release_tag="$(printf '%s' "$response" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
  [[ "$release_tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || \
    die "latest release did not return a stable semver tag"
}

[[ -n "$release_tag" ]] || resolve_latest_tag
bundle_name="$PROJECT-$release_tag"
asset_base="https://github.com/$repository/releases/download/$release_tag/$bundle_name.tar.gz"
checksum_url="$asset_base.sha256"

tmp_dir="$(mktemp -d)"
archive="$tmp_dir/$bundle_name.tar.gz"
checksum="$tmp_dir/$bundle_name.tar.gz.sha256"
info "Downloading HomeLab Dashboard $release_tag..."
curl -fL --retry 3 --retry-delay 1 --proto '=https' --tlsv1.2 -o "$archive" "$asset_base"
curl -fL --retry 3 --retry-delay 1 --proto '=https' --tlsv1.2 -o "$checksum" "$checksum_url"
(cd "$tmp_dir" && sha256sum -c "$(basename "$checksum")")

extract_dir="$tmp_dir/extracted"
mkdir -p "$extract_dir"
tar -xzf "$archive" -C "$extract_dir"
bundle_root="$extract_dir/$bundle_name"
[[ -f "$bundle_root/compose.yml" && -f "$bundle_root/Containerfile" ]] || \
  die "release bundle is missing compose.yml or Containerfile"

mkdir -p "$install_dir"
if [[ -f "$install_dir/.env" ]]; then
  cp -p "$install_dir/.env" "$tmp_dir/.env.backup"
fi
cp -a "$bundle_root/." "$install_dir/"
if [[ -f "$tmp_dir/.env.backup" ]]; then
  install -m 0600 "$tmp_dir/.env.backup" "$install_dir/.env"
elif [[ -f "$install_dir/.env.example" ]]; then
  install -m 0600 "$install_dir/.env.example" "$install_dir/.env"
else
  install -m 0600 /dev/null "$install_dir/.env"
fi

env_set() {
  local key="$1" value="$2" file="$install_dir/.env" replacement="$tmp_dir/env.$$"
  awk -v key="$key" -v value="$value" '
    BEGIN { prefix = key "="; found = 0 }
    index($0, prefix) == 1 { print prefix value; found = 1; next }
    { print }
    END { if (!found) print prefix value }
  ' "$file" > "$replacement"
  install -m 0600 "$replacement" "$file"
  rm -f "$replacement"
}

env_set TS_AUTHKEY "${TS_AUTHKEY:-}"
env_set ADMIN_USERS "${ADMIN_USERS:-}"
env_set XDG_RUNTIME_DIR "$runtime_dir"
env_set DASHBOARD_IMAGE "${DASHBOARD_IMAGE:-ghcr.io/bnhminh1010/homelab-dashboard:$release_tag}"

export XDG_RUNTIME_DIR="$runtime_dir"
mkdir -p "$runtime_dir"
systemctl --user enable --now podman.socket
podman system migrate >/dev/null 2>&1 || true
test -S "$runtime_dir/podman/podman.sock" || die "rootless Podman socket was not created at $runtime_dir/podman/podman.sock"

if ((skip_host_agent == 0)); then
  info "Installing host-agent user service..."
  (cd "$install_dir" && ./deploy/host-agent/install.sh)
fi

compose() {
  (cd "$install_dir" && "${compose_cmd[@]}" --file compose.yml "$@")
}

info "Validating Compose configuration..."
compose config --quiet
info "Pulling dashboard image..."
compose pull dashboard
info "Starting HomeLab Dashboard..."
compose up -d
compose ps

info "Waiting for dashboard health..."
for _ in {1..40}; do
  if podman ps --all --filter label=com.docker.compose.service=dashboard --format '{{.Status}}' 2>/dev/null | grep -qi 'healthy'; then
    break
  fi
  sleep 3
done
if ! podman ps --all --filter label=com.docker.compose.service=dashboard --format '{{.Status}}' 2>/dev/null | grep -qi 'healthy'; then
  warn "dashboard did not report healthy within 120 seconds"
  compose logs --tail=80 dashboard >&2 || true
  exit 1
fi

cat <<EOF

HomeLab Dashboard $release_tag is running.
Install directory: $install_dir
Open the HTTPS URL shown by Tailscale Serve, or run:
  (cd "$install_dir" && ${compose_cmd[*]} ps)

Keep .env private (mode 0600). To update later:
  curl -fsSL https://github.com/$repository/releases/latest/download/install.sh | bash
EOF
