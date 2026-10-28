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

## Read-only Bot MCP

Set an independent `MCP_API_KEY` to enable stateless Streamable HTTP at `{APP_BASE_PATH}/mcp`. The four read-only tools return Chinese reports for AI consumption: `get_quota_summary`, `get_key_usage` (default exact alias `bot`), `get_model_tps` (global active-Key scope), and `get_quota_recovery_events` (persistent event cursor). An empty key disables MCP; web login and the external quota API retain their separate permissions. Reads reuse stored fees and quota estimates, never refresh providers, and respect the cost-recalculation read gate. The hourly reader uses a single database snapshot, existing Overview rollups and disjoint raw-event intervals to preserve the exact trailing 24 hours, including aggregation lag. The recovery detector adds one event table; baselines, reset barriers and below-threshold hits stay in memory. No frontend changes are required.

See [Bot MCP integration](examples/bot-mcp/README.md) for configuration, calls, report examples, and statistical definitions.

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

### Multi-account Weekly recovery observations

The Codex detector compares adjacent, non-overlapping real official quota queries only. Before natural expiry (with a 120-second margin), any raw remaining increase plus an absolute Weekly reset extension over 120 seconds is a hit. Three distinct nonempty AccountIDs within 30 minutes persist one incident with the first three masked evidence records; successful persistence starts a 30-minute quiet period. Duplicate credential files do not add votes. Events describe observations, not official compensation or proof against external card consumption.

Keeper consumption establishes an account-wide in-memory generation barrier before the call. Known outcomes rebaseline on the next valid official read. Unknown outcomes enqueue one read-only verification using existing refresh workers; they never retry consumption. Unchanged or failed verification leaves the barrier until recovery or the previous Weekly boundary. Restart learns fresh baselines and cannot reconstruct old reset barriers. `get_quota_recovery_events` passively returns Chinese text plus structured cursor pages; only threshold incidents survive restarts.

## Upstream maintenance

The 2026-10-28 integration baseline is upstream main `4cfffa1c2feea8e672dd2b739777f69a593b250f` (v2.0.2 plus subsequent fixes). Merge upstream history into a temporary branch from `fork`, resolve conflicts without dropping fork changes, run validation, then fast-forward `fork` and publish its image. Do not rebase published fork history or force-push either long-lived branch. `main` tracks upstream independently; syncing it never publishes the fork image.

Daily synchronization runs from default branch `main` at 08:17 Asia/Shanghai (GitHub may delay scheduled jobs). Configure repository Actions secret `UPSTREAM_SYNC_TOKEN` with a fine-grained PAT limited to this repository and Contents/Workflows read-write permissions. The workflow explicitly fails when the secret is missing and does not fall back to `GITHUB_TOKEN`. Token expiry, permission failures and merge conflicts require manual maintenance; never use force-sync to remove the maintenance workflow. Keep credentials out of git and logs.

Validation for each integration:

```sh
go test ./cmd/... ./internal/... -count=1 -timeout=300s
go vet ./cmd/... ./internal/...
go test -race ./internal/quota ./internal/quota/test ./internal/api/test ./internal/repository/test -run 'TestRecovery|TestWeeklyRecovery|TestQuotaSummary|TestSummary|TestMCP|TestExternal|Test.*UsageHeader|Test.*LimitReached|Test.*Bot' -count=1 -timeout=180s
npm --prefix web ci
npm --prefix web run test
npm --prefix web run lint
npm --prefix web run typecheck
npm --prefix web run build
node --test examples/account-quota-panel/panel.test.mjs examples/account-quota-panel/poller.test.mjs
node --test examples/account-quota-panel/browser-verify.mjs
```

The browser check uses `agent-browser` and requires its Chromium runtime. Run backend builds and workflow lint as well. The service-tier upgrade regression uses a disposable copy of a prior-fork-schema fixture and checks that quota capacity, snapshots, recovery evidence, cursors and stored fees survive. New upstream fields do not backfill historical tiers or recalculate old token accounting.

Fork builds publish `ghcr.io/zesuy/cpa-usage-keeper:latest` and `sha-<first-seven-commit-characters>` for linux/amd64, using the upstream Go/Node builder series and pulling current bases. Verify the build result, image revision and anonymous pull before handing over the fixed tag. Production updates remain manual: back up the database first, retain the previous image tag, and restore the matching backup if rollback requires reverting schema changes.
