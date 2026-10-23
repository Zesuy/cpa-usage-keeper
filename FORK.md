# Fork maintenance

- `main` follows `Willxup/cpa-usage-keeper:main`, with one additional upstream-sync workflow.
- `fork` contains our custom changes and publishes the fork image. Upstream changes enter this branch only when deliberately merged and verified.
- `backup/main-before-sync-20261023` preserves the old local history, including the superseded Antigravity implementation. This backup is local only.

## Docker image

Pushes to `fork`, or manual runs of **Publish fork Docker image** with `fork` selected, publish:

```text
ghcr.io/zesuy/cpa-usage-keeper:latest
ghcr.io/zesuy/cpa-usage-keeper:sha-<short-sha>
```

The image retains the original `linux/amd64` target and uses the current upstream Dockerfile. `main` pushes do not publish this image. GitHub package visibility must allow your deployment to pull it.

## Quota display preferences

The main Settings page includes three independent Antigravity quota display switches: Gemini Models, Claude and GPT models, and Gemini 4 Argon. They default to on, support any combination including all off, and apply immediately to both the credential list and detail drawer. Choices are saved in the current browser only; quota fetching, caching, and statistics are unaffected.

## Upstream synchronization

**Sync upstream main** runs daily at 00:17 UTC (08:17 Asia/Shanghai), and can also be dispatched manually from `main`. GitHub may delay scheduled runs. It merges upstream into `main` without force, preserving the maintenance workflow; conflicts fail the run for manual resolution.

The workflow uses `GITHUB_TOKEN` by default with `contents: write`. If repository rules or upstream workflow-file updates require additional authorization, configure the repository Actions secret `UPSTREAM_SYNC_TOKEN` with a token authorized to update this repository's contents and workflows. A classic token requires `repo` and `workflow` scopes. Never commit the token. A sync failure remains visible in Actions rather than resetting the branch or dropping changes.

To update the custom branch after synchronization:

```bash
git fetch origin
git switch main
git merge --ff-only origin/main
git switch fork
git merge main
# Resolve conflicts and run checks appropriate to the incoming changes.
git push origin fork
```

For future customizations, make small independent commits on `fork`. They can then be cherry-picked onto a new upstream base if a rebuild of the custom branch becomes necessary. Do not cherry-pick the old Antigravity commits: upstream already provides that functionality.
