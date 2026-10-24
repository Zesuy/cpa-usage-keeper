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

## External quota panel

Set `QUOTA_SUMMARY_API_KEY` in the server environment to enable `GET {APP_BASE_PATH}/api/v1/external/quota/summary` with independent `Authorization: Bearer <key>` authentication. An empty key disables the route (404); missing/incorrect keys return 401 even when web login is disabled. The key grants only the aggregate read, without login sessions, refresh or credential-management access. The response is the existing summary JSON with `Cache-Control: no-store`; cost recalculation returns the existing `503 costs_busy` response. Reads never refresh providers, and no CORS or database migration is added.

The [independent panel example](examples/account-quota-panel/README.md) includes the Gemini-designed two-column HTML, server/host-only poller, and integration instructions. The host keeps the key and pushes raw summary data into the panel; the HTML does not fetch Keeper or hold credentials.

## Quota summary

The Usage page aggregates all enabled, active credential files across pages, independently of list filters. It includes Codex/GPT, Antigravity Gemini, and Antigravity Claude/GPT; Argon and special model-specific quotas are excluded. Each pool keeps 5h and Weekly separate. Summary rows also reuse the credential list’s reset-adjusted cumulative request, success/failure, input/cache-read token, and total token counters. Rates are calculated from the summed counters. Antigravity rows share the same participating files, so these cumulative metrics overlap and must not be added across pools; quota capacities remain separate. Each summary group returns canonical subscription plan counts for its enabled, active credential files, independently of window freshness; these reuse the individual credential subscription badges. The current/estimated switch reuses the individual credential display. Antigravity display preferences also apply here.

Individual estimates are calculated once on the backend using the existing token/cost-to-used-percentage formula, and returned to both the list and summary. Missing capacities fall back to the same file's last valid estimate, then the arithmetic mean for the same provider, plan, pool, and window. Unknown plans cannot borrow from peers. Only each file's own valid estimates are stored in `quota_capacity_estimates`; peer values never become samples. Cost recalculation invalidates these saved estimates and summary snapshots. The migrations are additive.

Successful refreshes and usage headers persist normalized per-window observations in `quota_summary_snapshots`, independently of refresh task state. Summary reads restore these snapshots after restart and retain them during pending or failed refreshes; they never query providers. The 15-minute threshold marks older observations as cached instead of discarding them. Once a known reset time is reached, the summary projects that file’s window as fully remaining with zero current usage until the next successful observation. Old capacity estimates can still be reused; projected zeros are never persisted as provider observations or estimate samples. Mixed reset/unreset files remain weighted by their capacities, and all-reset rows show 100% with the existing zero countdown. Invalid or missing reset/observation times remain unavailable rather than becoming full. Cache labels, the oldest contributing observation time, coverage, and specific missing-data reasons distinguish retained data from fresh results and real zero usage. The configured backend refresh schedule is unchanged. On the first upgrade, one successful refresh seeds the snapshots. Only normalized summary fields are saved, excluding raw responses and credential secrets. Percentages weight remaining capacity by estimated tokens; each 5h/Weekly row displays the earliest upcoming individual reset among its valid participating windows, using the same reset date and gray countdown rendering as individual credentials. Coverage, cache, filled-estimate status, and observation update times remain inside the info details.

Admin APIs: `GET /api/v1/quota/summary` reads the summary; `POST /api/v1/quota/summary/refresh` refreshes all eligible files through the existing bounded, deduplicated manual queue. Both follow the cost-read gate. The browser polls every 60 seconds, or 5 seconds while refresh tasks are pending.

An optional offline integration test accepts `KEEPER_SUMMARY_TEST_BACKUP=/path/to/completed-backup.db go test ./internal/quota -run TestQuotaSummaryFromProductionBackup -v`. It copies the backup before migration, reconstructs Codex observations from stored cycles and usage, and never starts a collector. Keep backups in ignored local directories.

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
