#!/usr/bin/env bash
set -euo pipefail

if (( EUID == 0 )); then
  echo "Refusing to install the host agent as root; run this script as the rootless dashboard user." >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
runtime_dir="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
image="localhost/homelab-host-agent-installer:local"
binary_dir="${HOME:?HOME is required}/.local/libexec"
unit_dir="$HOME/.config/systemd/user"
tmp_dir="$(mktemp -d)"
container_id=""

cleanup() {
  if [[ -n "$container_id" ]]; then
    podman rm --force "$container_id" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

command -v podman >/dev/null || { echo "podman is required" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemd is required" >&2; exit 1; }
[[ -x /bin/bash ]] || { echo "/bin/bash is required on the host" >&2; exit 1; }
[[ -d "$runtime_dir" ]] || { echo "XDG_RUNTIME_DIR does not exist: $runtime_dir" >&2; exit 1; }

echo "Building the static host agent with rootless Podman..."
podman build --target host-agent-export --tag "$image" "$repo_root"
container_id="$(podman create "$image")"
podman cp "$container_id:/homelab-host-agent" "$tmp_dir/homelab-host-agent"

install -d -m 0755 "$binary_dir" "$unit_dir"
install -d -m 0700 "$runtime_dir/homelab-dashboard"
install -m 0755 "$tmp_dir/homelab-host-agent" "$binary_dir/homelab-host-agent"
install -m 0644 "$script_dir/homelab-host-agent.service" "$unit_dir/homelab-host-agent.service"

export XDG_RUNTIME_DIR="$runtime_dir"
systemctl --user daemon-reload
systemctl --user enable homelab-host-agent.service >/dev/null
systemctl --user restart homelab-host-agent.service

socket="$runtime_dir/homelab-dashboard/agent.sock"
for _ in {1..30}; do
  [[ -S "$socket" ]] && break
  sleep 0.1
done
if [[ ! -S "$socket" ]]; then
  echo "Host agent started but did not create $socket" >&2
  echo "Inspect it with: journalctl --user -u homelab-host-agent.service -n 100" >&2
  exit 1
fi

echo "Installed $binary_dir/homelab-host-agent"
echo "Host agent socket: $socket"
echo "Next: enable HOST_SHELL_ENABLED and set HOST_SHELL_USERS in .env, then recreate dashboard."
