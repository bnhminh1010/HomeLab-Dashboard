# Contributing

Thanks for helping improve HomeLab Dashboard. The project intentionally stays
small: a Go backend, embedded static assets, and no Node.js build chain.

## Before you start

Open or discuss an issue before investing in a broad feature, security-sensitive
change, new dependency, or design rewrite. Small, focused bug fixes and
documentation improvements can go straight to a pull request.

Work from the current `develop` branch:

```bash
git checkout develop
git pull --ff-only
git switch -c fix/short-description
```

Use a short, lowercase branch prefix:

- `feat/` for a user-visible capability;
- `fix/` for a defect;
- `docs/` for documentation only; and
- `chore/` for maintenance with no product behavior change.

Keep one concern per branch and pull request. Do not mix a UI polish pass,
refactor, migration, and feature unless they are inseparable.

Open feature, fix, documentation, and maintenance pull requests against
`develop`. A release pull request is the only normal path from `develop` to
`main`. For an urgent production fix, branch `hotfix/*` from `main`, open its
pull request against `main`, then open a follow-up synchronization pull request
from `main` back to `develop`.

## Development checks

Run the smallest relevant test first, then the full suite before requesting
review:

```bash
go mod verify
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
go test -tags=browser ./tests/browser -count=1
podman compose config
```

Browser tests require a local Chromium/Chrome installation. For a UI change,
also check the dashboard manually at desktop and mobile widths. Do not add npm,
an external CDN, or a browser build step: local vendor bundles in `static/lib/`
are deliberate.

## Pull request checklist

Explain the problem, the chosen approach, and the verification performed.
Call out migrations, configuration changes, security implications, and manual
operator steps explicitly. Include screenshots or a short recording for a
visual change.

Before opening the pull request, confirm:

- [ ] The branch is rebased or fast-forwarded from current `develop` (or
      current `main` for a `hotfix/*`).
- [ ] Tests and static checks relevant to the change pass.
- [ ] New behavior has focused tests; changed behavior updates existing tests.
- [ ] Public APIs, environment variables, migrations, and deployment steps are
      documented when they change.
- [ ] No secrets, auth keys, private hostnames, Tailnet addresses, production
      logs, or database files are included.
- [ ] The diff is focused and avoids unrelated formatting churn.

## Production UI checklist

The dashboard is an operations surface, not a decorative landing page. For a
UI contribution, also confirm:

- [ ] Every control invokes a real, authorized operation or is omitted.
- [ ] Labels, counts, status colors, and timestamps are backed by current data;
      empty, loading, stale, and error states are explicit.
- [ ] The primary action is clear without gradients, glow, novelty icons, or
      surplus visual decoration.
- [ ] Keyboard focus, semantic labels, contrast, reduced motion, and touch
      targets are preserved.
- [ ] The layout remains usable at desktop, tablet, and narrow mobile widths.
- [ ] New UI uses the existing CSS tokens and vanilla JavaScript modules rather
      than adding a framework or duplicate component system.

## Code and review style

Prefer the smallest change that solves the verified problem. Keep boundaries
clear between collectors, persistence, HTTP handlers, and browser modules.
Avoid speculative abstractions and do not refactor unrelated code while fixing
a bug. Reviewers may ask for a narrower patch, a regression test, or a clearer
operator-facing explanation before merging.

By submitting a contribution, you agree to license it under the
[Apache License 2.0](LICENSE).
