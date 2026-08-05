#!/usr/bin/env bash
set -euo pipefail

if (( EUID == 0 )); then
  echo "Refusing to install the SMART helper as root; use the dashboard runtime user." >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
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

command -v systemctl >/dev/null || { echo "systemd is required" >&2; exit 1; }
command -v smartctl >/dev/null || {
  echo "smartmontools is required; install smartctl before enabling the helper." >&2
  exit 1
}

if [[ -n "${SMART_AGENT_BINARY:-}" ]]; then
  [[ -x "$SMART_AGENT_BINARY" ]] || { echo "SMART_AGENT_BINARY must point to an executable" >&2; exit 1; }
  install -m 0755 "$SMART_AGENT_BINARY" "$tmp_dir/homelab-smart-agent"
else
  command -v podman >/dev/null || {
    echo "Rootless Podman is required (or set SMART_AGENT_BINARY to a prebuilt binary)." >&2
    exit 1
  }
  image="localhost/homelab-smart-agent-installer:local"
  echo "Building the static SMART helper with rootless Podman..."
  podman build --target smart-agent-export --tag "$image" "$repo_root"
  container_id="$(podman create "$image")"
  podman cp "$container_id:/homelab-smart-agent" "$tmp_dir/homelab-smart-agent"
fi

install -d -m 0755 "$binary_dir" "$unit_dir"
install -m 0755 "$tmp_dir/homelab-smart-agent" "$binary_dir/homelab-smart-agent"
install -m 0644 "$script_dir/homelab-smart-agent.service" "$unit_dir/homelab-smart-agent.service"

systemctl --user daemon-reload
systemctl --user enable --now homelab-smart-agent.service
if ! systemctl --user is-active --quiet homelab-smart-agent.service; then
  echo "The service did not become active. Inspect it with:" >&2
  echo "  journalctl --user -u homelab-smart-agent.service -n 100" >&2
  exit 1
fi

echo "Installed $binary_dir/homelab-smart-agent"
echo "Socket: ${XDG_RUNTIME_DIR:-/run/user/$EUID}/homelab-dashboard/smart-agent.sock"
echo "If SMART reads are denied, configure udev/device permissions for this user; the dashboard will report UNAVAILABLE safely until then."
