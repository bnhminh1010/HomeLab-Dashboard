#!/usr/bin/env bash
set -euo pipefail

if (( EUID == 0 )); then
  echo "Refusing to install the node agent as root; use the rootless Podman account." >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
binary_dir="${HOME:?HOME is required}/.local/libexec"
config_dir="$HOME/.config/homelab-node-agent"
state_path="$config_dir/credentials.json"
unit_dir="$HOME/.config/systemd/user"
server_url="${1:-${HOMELAB_DASHBOARD_URL:-}}"
display_name="${2:-}"
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
if [[ -z "$server_url" && ! -f "$state_path" ]]; then
  echo "Usage: $0 https://dashboard.your-tailnet.ts.net [display-name]" >&2
  exit 1
fi

if [[ -n "${NODE_AGENT_BINARY:-}" ]]; then
  [[ -x "$NODE_AGENT_BINARY" ]] || { echo "NODE_AGENT_BINARY must point to an executable" >&2; exit 1; }
  install -m 0755 "$NODE_AGENT_BINARY" "$tmp_dir/homelab-node-agent"
else
  command -v podman >/dev/null || {
    echo "Rootless Podman is required (or set NODE_AGENT_BINARY to a prebuilt binary)." >&2
    exit 1
  }
  image="localhost/homelab-node-agent-installer:local"
  echo "Building the static node agent with rootless Podman..."
  podman build --target node-agent-export --tag "$image" "$repo_root"
  container_id="$(podman create "$image")"
  podman cp "$container_id:/homelab-node-agent" "$tmp_dir/homelab-node-agent"
fi

install -d -m 0755 "$binary_dir" "$unit_dir"
install -d -m 0700 "$config_dir"

if [[ ! -f "$state_path" ]]; then
  read -r -s -p "One-time node enrollment code: " enrollment_code
  echo
  if [[ -z "$enrollment_code" ]]; then
    echo "Enrollment code must not be empty." >&2
    exit 1
  fi
  printf '%s\n' "$enrollment_code" | "$tmp_dir/homelab-node-agent" enroll \
    --server "$server_url" \
    --display-name "$display_name" \
    --state "$state_path" \
    --code-stdin
  unset enrollment_code
else
  mode="$(stat -c '%a' "$state_path")"
  [[ "$mode" == "600" ]] || { echo "Credential file must have mode 0600, got $mode" >&2; exit 1; }
  echo "Keeping existing credential state at $state_path"
fi

install -m 0755 "$tmp_dir/homelab-node-agent" "$binary_dir/homelab-node-agent"
install -m 0644 "$script_dir/homelab-node-agent.service" "$unit_dir/homelab-node-agent.service"

systemctl --user daemon-reload
systemctl --user enable --now homelab-node-agent.service
if ! systemctl --user is-active --quiet homelab-node-agent.service; then
  echo "The service did not become active. Inspect it with:" >&2
  echo "  journalctl --user -u homelab-node-agent.service -n 100" >&2
  exit 1
fi

echo "Installed $binary_dir/homelab-node-agent"
echo "Credential state: $state_path"
echo "For boot without an interactive login, an administrator may enable lingering: loginctl enable-linger $USER"
