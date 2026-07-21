# Security policy

## Supported versions

This project is currently in its `0.x` release line. The latest commit on
`main` and the latest published `0.x` release are supported with security
fixes. Older revisions are not supported.

## Reporting a vulnerability

Please do not open a public issue for a suspected vulnerability.

Use [GitHub private vulnerability reporting](https://github.com/bnhminh1010/homelab-dashboard/security/advisories/new)
for this repository. Include a clear description, affected revision or
configuration, reproduction steps or proof of concept, expected impact, and
any mitigation you found.

Do not include real Tailscale auth keys, node credentials, ntfy tokens, host
paths, private IP addresses, container logs, or other production secrets in a
report. Redact them or provide a minimal synthetic reproduction.

## What to expect

We will acknowledge a private report, confirm whether it is in scope, and work
with the reporter on a fix and coordinated disclosure. Security fixes are
released through the supported `0.x` line. Please give maintainers reasonable
time to investigate and publish a remediation before public disclosure.

## Scope

Security-sensitive surfaces include authentication and authorization,
Tailscale identity-header trust, CSRF and same-origin mutation checks, service
probe SSRF boundaries, WebSocket/session handling, terminal/host-agent access,
node enrollment, secret handling, and configuration import/export.

Deployment remains the operator's responsibility: do not expose the dashboard
directly on `0.0.0.0` while trusting Tailscale identity headers, and protect
the rootless Podman socket and host-shell allowlists as privileged access.
