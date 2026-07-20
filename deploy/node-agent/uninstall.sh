#!/usr/bin/env bash
set -euo pipefail

if (( EUID == 0 )); then
  echo "Refusing to uninstall a user service as root." >&2
  exit 1
fi

purge=false
if [[ "${1:-}" == "--purge" ]]; then
  purge=true
elif [[ $# -gt 0 ]]; then
  echo "Usage: $0 [--purge]" >&2
  exit 1
fi

unit="$HOME/.config/systemd/user/homelab-node-agent.service"
systemctl --user disable --now homelab-node-agent.service >/dev/null 2>&1 || true
rm -f "$unit" "$HOME/.local/libexec/homelab-node-agent"
systemctl --user daemon-reload
systemctl --user reset-failed homelab-node-agent.service >/dev/null 2>&1 || true

if [[ "$purge" == true ]]; then
  rm -rf "$HOME/.config/homelab-node-agent"
  echo "Removed node agent, unit, and credentials. Re-enrollment will be required."
else
  echo "Removed node agent and unit. Credentials were retained; use --purge to revoke local state."
  echo "Revoke this node from the dashboard before retiring it permanently."
fi
