# Repository ruleset activation

The JSON files in `rulesets/` are importable repository-ruleset templates;
they do not change GitHub settings by themselves. Import them after the CI
workflows have run once, so GitHub can resolve the required check names.

## Import and activate

1. Open the repository on GitHub, then choose **Settings** → **Rules** →
   **Rulesets**.
2. Choose **New ruleset** → **Import a ruleset**, select one JSON file below,
   and review the resulting target and enforcement before saving.
3. Import `rulesets/develop.json` for `refs/heads/develop`, then import
   `rulesets/main.json` for `refs/heads/main`.
4. In each imported ruleset, open **Require status checks to pass** and verify
   the check contexts exactly match the CI job names: `Go quality`, `Go race`,
   `Browser UI`, `Container build and Compose config`, and `Govulncheck`.
   `main` additionally requires `Release source` and `CodeQL`. `Release source`
   runs only for pull requests targeting `main` and accepts only `develop` or
   `hotfix/*` as their source branch. Do not rename those jobs without updating
   both JSON templates and the active GitHub rulesets.
5. The templates intentionally require a pull request but set the required
   approval count to zero and disable last-push approval. CI—not a mandatory
   human review—is the merge gate selected for this repository.
6. For `main`, retain the CodeQL code-scanning rule with Code Scanning Alerts
   at **High or higher** and Other Alerts at **Errors**. GitHub only exposes
   this control for repositories/plans where code-scanning merge protection is
   available. If it is unavailable, retain the required `CodeQL` status check
   and enable the code-scanning rule when GitHub exposes it.
7. Import `rulesets/v-tags.json` for `refs/tags/v*`. It makes version tags
   immutable after creation. Create a replacement tag only through an explicit
   administrator remediation process; do not silently retag a release.

## Release source enforcement

`workflows/release.yml` runs only when a `v*` tag is pushed. Before it creates
a GitHub release, it fetches `origin/main` and requires the tagged commit to be
reachable from that branch. A tag created from any other history therefore
cannot publish generated release notes. This complements the `main` pull
request gate rather than replacing it.

The release workflow needs `contents: write` to publish generated notes and
`security-events: write` to upload the tagged CodeQL analysis. No personal
GitHub token, ruleset, or remote repository configuration is applied by this
repository. The source check is intentionally performed again in CI because a
ruleset cannot express the ancestry relationship between a tag and a branch.
