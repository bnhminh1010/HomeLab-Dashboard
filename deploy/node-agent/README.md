# Homelab node agent

`homelab-node-agent` adds one remote Linux/Podman host to the dashboard. It is
a rootless systemd user service and makes one outbound WebSocket connection to
the dashboard over Tailscale HTTPS. The node does not expose an inbound port.

## Install

1. In the dashboard node-management screen, create a one-time enrollment code.
   It expires after 10 minutes and can be used once.
2. On the remote node, as the same unprivileged account that owns the rootless
   Podman socket, run:

   ```bash
   ./deploy/node-agent/install.sh https://dashboard.your-tailnet.ts.net "rack-node-1"
   ```

   The installer prompts for the enrollment code without echoing it. Rootless
   Podman builds and extracts a static binary from the `node-agent-export`
   image stage (no host Go toolchain and no Node.js), installs it in
   `~/.local/libexec`, stores
   credentials in `~/.config/homelab-node-agent/credentials.json` with mode
   `0600`, and enables `homelab-node-agent.service`.

For a prebuilt binary, set `NODE_AGENT_BINARY=/absolute/path/to/homelab-node-agent`.
For non-interactive enrollment, pipe the code to the binary rather than putting
it in shell history:

```bash
printf '%s\n' "$ONE_TIME_CODE" | homelab-node-agent enroll \
  --server https://dashboard.your-tailnet.ts.net \
  --state "$HOME/.config/homelab-node-agent/credentials.json" \
  --code-stdin
```

## Runtime and security model

- HTTPS/WSS is mandatory. Plain HTTP/WS is accepted only for a loopback
  development server.
- Heartbeats and host/container snapshots are sent every 10 seconds. A lost
  connection retries with exponential backoff capped at 30 seconds.
- The agent accepts only protocol-v1 typed operations: container logs,
  container `/bin/sh`, current-user host `/bin/bash --login`, stream input,
  resize, and cancel. There is no arbitrary command field.
- Protected or stopped containers are rejected by the Podman API client.
- A disconnect cancels all streams. Host shells are never resumed automatically.
- The agent refuses to start as root. The host shell therefore has the same
  filesystem, network, rootless Podman, and optional `sudo` access as an
  ordinary login shell for the systemd user running the agent. The unit does
  not use `ProtectSystem`, `PrivateTmp`, or `NoNewPrivileges`, because those
  controls would silently turn the advertised host terminal into a restricted,
  read-only shell.
- At most four streams are active by default (configurable from one to eight),
  and outbound buffering is bounded.

That host-shell equivalence is an intentional security tradeoff. Only dashboard
administrators should be allowed to open it, and revoking the node or stopping
the user service immediately removes the remote shell path.

Useful commands:

```bash
systemctl --user status homelab-node-agent.service
journalctl --user -u homelab-node-agent.service -f
systemctl --user restart homelab-node-agent.service
```

If the service must start at boot while the account is logged out, an
administrator can run `loginctl enable-linger USER`.

## Backup status reports

The agent can report the freshness of any backup program without running that
program or receiving its credentials. Set `BACKUP_STATUS_FILE` to an absolute,
readable JSON path in a systemd user drop-in:

```ini
# ~/.config/systemd/user/homelab-node-agent.service.d/backup.conf
[Service]
Environment=BACKUP_STATUS_FILE=%h/.local/state/homelab-backup/status.json
```

Then reload and restart the service:

```bash
systemctl --user daemon-reload
systemctl --user restart homelab-node-agent.service
```

Each job writes either one object or a wrapper containing `backups`. Write to a
temporary file in the same directory and rename it so the agent never reads a
partial update:

```json
{
  "backups": [
    {
      "job": "restic-home",
      "status": "success",
      "completedAt": "2026-07-22T03:15:00Z",
      "expectedWithinSeconds": 93600,
      "bytes": 158734221,
      "message": "daily snapshot complete"
    }
  ]
}
```

`status` is one of `success`, `failed`, `running`, or `unknown`. A successful
entry needs `completedAt`; the optional `expectedWithinSeconds` defines when a
successful backup becomes overdue. The document is capped at 64 KiB and 50
jobs. For example, a shell backup script can publish it atomically with:

```bash
state_dir="$HOME/.local/state/homelab-backup"
mkdir -p "$state_dir"
temporary="$(mktemp "$state_dir/status.json.XXXXXX")"
printf '%s\n' '{"job":"restic-home","status":"success","completedAt":"2026-07-22T03:15:00Z","expectedWithinSeconds":93600}' >"$temporary"
mv -f "$temporary" "$state_dir/status.json"
```

## Uninstall

Revoke the node in the dashboard first, then run:

```bash
./deploy/node-agent/uninstall.sh
```

This keeps the local credential file for a reversible reinstall. Use
`./deploy/node-agent/uninstall.sh --purge` to remove it permanently.
