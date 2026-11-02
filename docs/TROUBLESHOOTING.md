<h1><img src="../assets/brand/logo-symbol.svg" width="36" height="36" alt="" align="top"> Troubleshooting</h1>

Symptom → cause → fix for the problems you are most likely to hit, plus the exit-code reference.

[← Back to README](../README.md) · [Docs index](README.md)

---

> [!TIP]
> Start with `credorbit doctor`. It checks the AWS config, SSO sessions and tokens, discovery age, file permissions, CA files, proxy, TLS interception, rotation file and shell integration, and prints a `fix:` line for every problem. Add `-v` to any command for debug logging in `credorbit.log`.

## Quick index

| Symptom | Jump to |
|---|---|
| `x509: certificate signed by unknown authority` | [TLS](#tls-certificate-signed-by-unknown-authority) |
| `SSO session "…" is not logged in` / exit code 4 | [Token expired](#token-expired-or-not-logged-in) |
| `skipped: no access` vs `skipped: transient` | [No access vs transient](#no-access-vs-transient-errors) |
| `refusing to remove anything` | [80 % safeguard](#stale-profiles-not-removed) |
| `file is locked by another CredOrbit process` | [Locked file](#file-is-locked) |
| Windows ACL warnings | [Windows ACLs](#windows-acl-warnings) |
| Tools don't see the credentials | [Credentials not picked up](#credentials-not-picked-up) |
| `no eligible rotation account` | [Rotation](#rotation-no-eligible-accounts) |
| Bedrock `AccessDeniedException` | [Bedrock](#bedrock-access-denied) |
| `not applied: non-interactive` | [Non-interactive runs](#nothing-happens-in-scripts-or-ci) |

## TLS: certificate signed by unknown authority

| | |
|---|---|
| **Symptom** | Login, discovery or export fails with `certificate signed by unknown authority`; `doctor` shows `[fail] network TLS: certificate not trusted` |
| **Cause** | A TLS-intercepting proxy or SASE gateway re-signs HTTPS traffic with your organisation's CA, which is not in the system trust store |
| **Fix** | Add the organisation's root CA. It is appended to the system roots, so it keeps working off the corporate network |

```sh
credorbit config set network.extra_cas ~/certs/acme-root-ca.pem
credorbit doctor     # expect: "TLS-intercepting network detected (issuer …); trusted via configured CA"
```

- Already have a bundle in `AWS_CA_BUNDLE`? CredOrbit uses it automatically when `network.ca_bundle` is empty.
- For the AWS CLI and other tools that **replace** their trust store with `AWS_CA_BUNDLE`, write a combined bundle (system roots + your CAs) and point them at it:

```sh
credorbit ca bundle --out ~/.aws/combined-ca.pem
export AWS_CA_BUNDLE=~/.aws/combined-ca.pem
```

- `doctor` reports a CA file that is missing, unreadable or has no certificates by name. Fix the path rather than removing the setting.
- Need to ignore `network.extra_cas` for one run (the legacy `SASE_CA=none`)? Use `--no-extra-ca`.

> [!WARNING]
> CredOrbit never disables certificate verification, and there is no option to. Getting an "unknown authority" error means a CA is missing.

## Token expired or not logged in

| | |
|---|---|
| **Symptom** | `SSO session "acme" is not logged in (run: credorbit login --session acme)`, `sign in with: credorbit login`, exit code **4**, or every profile shows `token expired` |
| **Cause** | The access token expired and could not be refreshed (no refresh token, or the client registration expired) |
| **Fix** | `credorbit login` (add `--device` without a local browser, `--force` to start over) |

`credorbit status` and `doctor` show whether a session is logged in, expired but silently refreshable, or signed out. A refreshable token is renewed automatically by the next command.

Other session errors:

| Message | Fix |
|---|---|
| `no SSO session configured` | `credorbit login --name acme --start-url https://d-1234567890.awsapps.com/start --region us-east-1` or `credorbit import` |
| `several SSO sessions (a, b): pass --session …` | `--session NAME`, `SSO_SESSION=NAME`, or `credorbit config set sso.default_session NAME` |
| `profiles without session` (doctor) | Add the missing `[sso-session]` block or fix `sso_session` in those profiles |

## No access vs transient errors

Each failed profile is classified:

| Label | Meaning | What to do |
|---|---|---|
| `no access` | IAM Identity Center returned `ForbiddenException`: the role or account is no longer assigned to you | Check your assignments; `credorbit roles list PROFILE`; pick another role with `credorbit roles set PROFILE ROLE`; or `--policy strongest` |
| `transient` | Throttling, 5xx or network trouble, after automatic retries with backoff | Run again; lower `--parallel` if you are being throttled |
| `token expired` | The token became invalid | `credorbit login` |

Only `no access` is treated as revoked access. A failed or partial discovery never marks accounts as missing, and failed profiles keep their previous credentials (`Kept previous credentials for: …`).

## Stale profiles not removed

| Message | Cause | Fix |
|---|---|---|
| `discovered N accounts but M are configured (< 80%) — refusing to remove anything` | Mass-removal safeguard: discovery returned too few accounts compared with your profiles | Usually a partial outage or wrong session: run `credorbit discover` again later. If the drop is real, lower `sync.threshold` for that run and review carefully |
| `discovery incomplete: …` / `discovery is cached/offline` / `cached discovery: refresh to evaluate removals` | Live discovery failed; CredOrbit is working from the cache | Fix connectivity or login, then re-run |
| `N stale profile(s) kept (removal not approved; pass --allow-remove to remove)` | Removals need explicit approval, and `--yes` never gives it | Run interactively and answer `y`, or add `--allow-remove` |
| Nothing proposed at all | `sync` only plans removals with `--remove-stale` (or `--allow-remove`); `sync.remove_stale=never` disables them | `credorbit sync --remove-stale` |

Only profiles that start with `profiles.prefix` are ever proposed for removal.

## File is locked

| | |
|---|---|
| **Symptom** | `~/.aws/credentials: file is locked by another CredOrbit process` |
| **Cause** | Another CredOrbit run (another terminal, cron, the TUI) is writing the same file; CredOrbit waits up to 10 seconds |
| **Fix** | Let the other run finish and retry. The `<file>.lock` file may stay on disk; it is harmless, and the lock is released when the process exits |

## Windows ACL warnings

| | |
|---|---|
| **Symptom** | `doctor`: `credentials file … ACL grants access to a broad group (Everyone/Users)` with `fix: restrict … to your user (icacls)` |
| **Cause** | The file was created by another tool or inherited permissions from its folder |
| **Fix** | Run any CredOrbit command that rewrites the file (export, sync), which applies an owner-only ACL, or restrict it yourself |

```powershell
icacls "$env:USERPROFILE\.aws\credentials" /inheritance:r /grant:r "${env:USERNAME}:F"
```

On macOS and Linux the equivalent is `chmod 600 ~/.aws/credentials`.

## Credentials not picked up

| Check | Fix |
|---|---|
| Is export enabled? In Normal mode the credentials file is **not** written (`credentials verified but not written (export.enabled=false)`) | Use the SSO profile directly (`aws --profile p_acme-dev …`, SDKs resolve it from the token cache), or `credorbit config set export.enabled true` |
| Which section name? With `export.strip_prefix=true`, profile `p_acme-dev` is written as `[acme-dev]` | Use `AWS_PROFILE=acme-dev`, or set `export.strip_prefix false` |
| Which file? `CREDS_FILE`, `export.credentials_file` or `AWS_SHARED_CREDENTIALS_FILE` may point elsewhere | `credorbit status` prints the credentials path in use |
| Expired? Exported credentials expire with the role session | `credorbit status` / `doctor` list expired sections; re-run `credorbit export` |
| Was the profile in scope? `export` defaults to every profile **with the prefix** | `--profile NAME`, `--group main`, or `--all --any-prefix` |
| `[default]` not refreshed | Set `export.default_profile`; it is skipped in single-profile runs, and kept unchanged if that profile failed |

## Rotation: no eligible accounts

| | |
|---|---|
| **Symptom** | `no eligible rotation account (none has valid credentials) — acme-bedrock-01: expired at …; … (run credorbit export to refresh credentials)` |
| **Cause** | A rotation account is eligible only if its section in the credentials file has complete, unexpired credentials (with a 5-minute margin) |
| **Fix** | Enable export and refresh: `credorbit config set export.enabled true` then `credorbit export --group main` (or `setup --export`) |

| Other message | Fix |
|---|---|
| `no rotation accounts configured` | `credorbit rotate accounts set acme-bedrock-01,acme-bedrock-02` |
| `manual strategy: use --set` | `credorbit rotate --set acme-bedrock-02`, or change `rotation.strategy` |
| `"x" is neither a rotation account nor a section of …` | Check the section name in the credentials file (prefix stripped by default) |
| Claude Code still uses the old account | Open a new shell or restart Claude Code; add `credorbit rotate shell` output to your shell profile. `doctor` reports when `AWS_PROFILE` differs from the rotation file |

## Bedrock access denied

| | |
|---|---|
| **Symptom** | `bedrock verification failed: …: no Bedrock access in this account/region (us-east-1): …` |
| **Cause** | The role cannot call Bedrock in that region, the model is not enabled for the account, or the model ID is wrong |
| **Fix** | Check model access for the account in the Bedrock console, the region in `rotation.bedrock_region`, and `rotation.bedrock_model` (use an inference-profile ID where your organisation requires one) |

- Verification needs both settings: `bedrock verification needs rotation.bedrock_region and rotation.bedrock_model`.
- During a strategy rotation, accounts that fail verification are skipped and the next eligible one is tried; their reasons are listed if none passes.

## Nothing happens in scripts or CI

Without a terminal, CredOrbit never waits for input:

| Output | Meaning | Fix |
|---|---|---|
| `not applied: non-interactive — re-run with --yes to apply` | A plan (sync, setup, import, role updates) was shown but not approved | Add `--yes` |
| `Remove N stale profiles? declined (non-interactive; pass --allow-remove)` | Destructive change not approved | Add `--allow-remove` |
| `multiple roles; choose with credorbit roles set` | A new account has several roles and nobody could choose | `credorbit roles set ACCOUNT ROLE`, then sync again |
| `groups edit needs an interactive terminal` | The editor is TUI-only | `credorbit groups add\|remove NAME KEY…` |

## Configuration problems

| Message | Fix |
|---|---|
| `unknown config keys in …` (doctor) | Fix typos; `credorbit config list` shows valid keys |
| `config written by newer CredOrbit` | Upgrade CredOrbit |
| `refusing to save invalid config: …` | Correct the value; every problem is listed |
| `duplicate sections` (doctor) | Merge duplicate `[sections]` in `~/.aws/config`; the AWS CLI rejects such files |
| `state reset` (doctor) | Nothing to do; the discovery cache is rebuilt by `credorbit discover` |

A backup of every changed AWS file is next to it as `<file>.credorbit-<timestamp>.bak`.

## Exit codes

| Code | Meaning | Examples |
|---|---|---|
| `0` | Success | |
| `1` | Error | Network or file error, a declined change, `doctor` found a failing check, no profile succeeded |
| `2` | Usage error | Unknown command or flag, wrong arguments, conflicting scope flags, unknown setting |
| `3` | Partial success | `export`: some profiles failed and some succeeded; `setup`: any profile failed |
| `4` | Needs login | The SSO token is expired or invalid; run `credorbit login` |
| `130` | Cancelled | `Ctrl-C` or `SIGTERM` |

## Getting help

Include `credorbit version`, the output of `credorbit doctor --json` and the relevant lines of `credorbit.log` (in `credorbit config path`'s directory) when you open an issue. These contain no secrets, but replace account IDs, start URLs and names with placeholders before sharing.
