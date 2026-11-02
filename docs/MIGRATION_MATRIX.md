# Feature-Parity Matrix — legacy `populate-aws-creds.sh` → CredOrbit

The legacy Bash script (with embedded Python and AWS CLI calls) is the behavioural
source of truth. Every capability below is carried over; where the script had a
defect, CredOrbit keeps the intent and fixes the defect. CredOrbit never modifies
or deletes the script; `credorbit import` only reads it.

Legend: ✅ parity · ⬆ parity + improvement · 🛠 legacy defect fixed

## Capabilities

| # | Script capability | Script mechanism | CredOrbit | Status |
|---|---|---|---|---|
| 1 | SSO login when token missing/expired | `aws sts get-caller-identity --profile <hardcoded>` then `aws sso login` | Native OIDC (browser auth-code + PKCE, or device code); token validity read from the cache, refresh-token renewal without a browser | ⬆ |
| 2 | Session reuse | AWS CLI cache | Same AWS CLI v2 cache format (`~/.aws/sso/cache/<sha1>.json`) — CLI, SDKs and CredOrbit share one login | ✅ |
| 3 | `FORCE_LOGIN=1` | env | `credorbit login --force`, `setup --full --force-login`, env `FORCE_LOGIN=1` honoured | ✅ |
| 4 | `SSO_SESSION` | env, default compiled in | `--session`, `sso.default_session`, env `SSO_SESSION`; first-run picker over `[sso-session]` blocks; multiple sessions | ⬆ |
| 5 | Corporate TLS interception (SASE) | curl probe with corp CA → export `AWS_CA_BUNDLE` to the corp CA only | System roots **plus** configured CA in one TLS pool — works on and off the corporate network without toggling; `doctor` shows interception; optional combined PEM for the AWS CLI | ⬆🛠 |
| 6 | Honour explicit `AWS_CA_BUNDLE` | env check | Honoured (appended to system roots) | ✅ |
| 7 | `SASE_CA=none` | env | `network.extra_cas: []` / `--no-extra-ca` | ✅ |
| 8 | Account discovery | `aws sso list-accounts` | `ListAccounts` with pagination, retries, bounded concurrency for roles | ✅ |
| 9 | Role discovery | `list-account-roles` per profile at export time | Once per discovery, cached (offline view), per-account error tracking | ⬆ |
| 10 | Add newly discovered accounts as `p_<name>` profiles | Python regex append | `profilesync` plan → preview → approve → apply; naming `prefix + name (space/_/+ → -)`, collision → `-<id>` | ✅ |
| 11 | Stale profile detection | portal ∖ config | Same, per session, legacy inline-SSO profiles included | ✅ |
| 12 | 80 % mass-removal safeguard | `len(portal) < 0.8 * len(have)` | Same threshold (configurable) **plus** block on incomplete/cached/failed discovery | ⬆ |
| 13 | Remove stale profiles | automatic | Reviewed: preview + separate confirmation (`--allow-remove` for automation; `--yes` never approves removals); never on failed discovery | 🛠 |
| 14 | Role ranking | Python `rank()` | `roles.DefaultRanker()` reproduces it exactly; ranking rules configurable | ✅ |
| 15 | `FALLBACK_ROLES` | env, default list | `roles.fallbacks` (imported), `--fallback-role` | ✅ |
| 16 | Try assigned roles strongest-first, then configured, then fallbacks | per-profile loop | `script` role policy (compat mode) | ✅ |
| 17 | Auto-update `sso_role_name` to the role that worked (`ROLE_UPDATE`) | regex rewrite of config | `roles.update_config` / `--update-roles`; shown in preview; compat mode enables it | ✅ |
| 18 | Credentials export `p_x` → `[x]` | build temp file | `export.strip_prefix` (default true) | ✅ |
| 19 | `[default]` copied from a fixed profile | hardcoded profile | `export.default_profile` (imported, not compiled in) | ✅ |
| 20 | Single-profile mode (`script.sh p_x`) | replace one section | `credorbit export --profile p_x` | ✅ |
| 21 | Main mode (`script.sh main`) | hardcoded `MAIN_PROFILES` | `--group main`; Main group managed in TUI/CLI, imported once from the script | ⬆🛠 |
| 22 | Newly discovered accounts included in main-mode export | `added_profiles` appended | `export.include_new_in_main` (compat mode) | ✅ |
| 23 | All mode | every `p_*` profile | `--all` (every SSO profile with the prefix, or `--any-prefix`) | ✅ |
| 24 | `PARALLEL` | busy-wait job loop | worker pool, `export.concurrency` / `--parallel`, env `PARALLEL` | ⬆ |
| 25 | `CREDS_FILE` | env | `export.credentials_file`, env `CREDS_FILE`, `AWS_SHARED_CREDENTIALS_FILE` | ✅ |
| 26 | `SYNC=0` skip sync | env | `--no-sync`, env `SYNC=0` | ✅ |
| 27 | Remove deleted accounts from `MAIN_PROFILES` / `CLAUDE_ROTATION` | script **rewrites its own source** | Group/rotation membership stored in `config.yaml`; missing accounts flagged, removal reviewed | 🛠 |
| 28 | Claude account rotation, random, avoid repeat | `while next == current` loop | `rotate` with random / round-robin / manual; eligibility = valid credentials | ⬆🛠 |
| 29 | Rotation skips accounts without credentials | grep creds file | `rotate.Eligible` also checks expiry | ⬆ |
| 30 | `~/.claude-aws-account` | write file | Same file and format (compatible with existing shell integration) | ✅ |
| 31 | Interrupted run leaves credentials untouched | temp file + `mv` | Same-dir temp + fsync + atomic rename, file lock, timestamped backups | ⬆🛠 |
| 32 | Never write empty credentials | `written == 0` check | Same, plus failed profiles keep their previous section | ⬆🛠 |
| 33 | Config backup | single rolling `config.bak` | Timestamped backups with retention (`export.backup_keep`) for config and credentials | ⬆ |
| 34 | Progress output | `[n/total] profile done (role)` | Live progress (TUI) / same line format (CLI), success/failure counters, report | ⬆ |
| 35 | Skip reason per profile | truncated to 200 chars | Full, classified reason (no access / token expired / transient) | ⬆ |

## Legacy defects and security weaknesses fixed

| Defect in the script | Impact | CredOrbit fix |
|---|---|---|
| **SSO access token passed as a command-line argument** to `aws sso get-role-credentials --access-token …` | Token visible to any local user via `ps`/process listings | In-process SDK calls; tokens never leave memory except the 0600 cache |
| Script **edits its own source** to drop removed accounts | Self-modifying code; diffs and updates break; races with a running copy | Membership in `config.yaml`; script never touched |
| `while [ "$next" == "$current" ]` rotation loop | **Infinite loop** when only one rotation account exists and it is current | `rotate.Next` returns the single eligible account; 0 eligible → clear error |
| Full/main mode **rewrites the whole credentials file** with only the processed profiles | Main mode silently deletes every non-main section (and any hand-written static keys) | Selective update: only processed sections replaced; unrelated sections preserved byte-for-byte |
| Single-profile mode prunes every section without a matching `p_` profile | Deletes unrelated user credential sections | Only sections CredOrbit wrote (tracked in `state.json`) are ever pruned, and only once their profile no longer exists |
| Partial failure drops the failed profiles' sections | A transient error removes working (still valid) credentials | Failed targets keep their previous section |
| Temp dir in `/tmp` then `mv` to `~/.aws` | Not atomic across filesystems; partial file possible | Temp file in the target directory + fsync + rename |
| No locking | Two runs (e.g. two terminals) interleave writes | Advisory lock on config/credentials |
| Strongest role chosen and persisted silently | Unreviewed privilege escalation in `~/.aws/config` | Normal mode uses the configured role; ranking is opt-in; persisting a role change is previewed/approved |
| Stale profiles removed automatically | An org change or API hiccup above 80 % removes profiles without review | Removal always previewed and confirmed; blocked on failed/incomplete discovery |
| Hardcoded account lists, default profile, session name, CA path, probe URL | Rebuild/edit needed for every change; leaks org data | Discovered or configured at runtime; nothing org-specific in the binary |
| Token validity tested with `sts get-caller-identity` on one hardcoded profile | Wrong answer if that one account is revoked | Token expiry read from the cache; refresh when possible |
| Corp CA used **instead of** system roots | Breaks on endpoints not intercepted (e.g. off the corporate network) | Corp CA appended to system roots |
| SSO token picked by newest cache file matching start URL | Can pick the wrong session's token | Cache file keyed by session name (AWS CLI scheme) and start URL verified |
| Only one rolling `config.bak` | A second bad run overwrites the only good backup | Timestamped backups, retention |
| Profile/role rewrites with regex | Comments/sub-properties inside a block can be lost; duplicate-key edge cases | Round-trip-preserving INI editor with validation after edit |
| Busy-wait `sleep 0.2` job control | CPU churn; no cancellation | Worker pool with context cancellation (Ctrl-C safe) |
| Error output truncated (`cut -c1-200`) | Root cause hidden | Full classified errors; secrets redacted |

## Environment variables still honoured

`SSO_SESSION`, `FORCE_LOGIN`, `PARALLEL`, `CREDS_FILE`, `SYNC`, `ROLE_UPDATE`,
`FALLBACK_ROLES`, `AWS_CA_BUNDLE`, `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`,
`HTTPS_PROXY`/`NO_PROXY`, `NO_COLOR`. They override `config.yaml` for that run only.
