<h1><img src="../assets/brand/logo-symbol.svg" width="36" height="36" alt="" align="top"> Security</h1>

What CredOrbit protects, what it writes where, how secrets are handled, and how to report a vulnerability.

[← Back to README](../README.md) · [Docs index](README.md)

---

> [!IMPORTANT]
> Please report vulnerabilities privately through **[GitHub Security Advisories](https://github.com/PiniPP/credorbit/security/advisories/new)**, not in public issues. See [Reporting a vulnerability](#reporting-a-vulnerability).

## Threat model

CredOrbit runs as your user on your workstation or CI runner and handles short-lived AWS credentials and an IAM Identity Center access token.

| Asset | Threat | Mitigation |
|---|---|---|
| SSO access and refresh token | Other local users or processes read it | Kept in memory and in the AWS CLI token cache only; files are 0600 (owner-only ACL on Windows); never passed as a process argument |
| Temporary role credentials | Leaked through logs, terminal output or JSON | Never logged or printed; `--json` output and previews contain no secrets; a redacting log handler scrubs every record |
| `~/.aws/config`, `~/.aws/credentials` | Corrupted by a crash, a concurrent run or a bad edit | Advisory lock, re-read under the lock, validation by re-parsing, timestamped backup, same-directory temp file, fsync, atomic rename |
| Unrelated profiles and credential sections | Deleted or rewritten | Only the sections CredOrbit wrote (tracked in `state.json`) can ever be pruned; everything else is preserved byte-for-byte |
| Privilege level | A stronger role silently persisted | Normal mode uses the configured role; persisting a different role is previewed and approved |
| Access to accounts | A discovery hiccup removes profiles | Removal is reviewed, needs explicit approval, and is blocked on incomplete or cached discovery and by the mass-removal safeguard |
| TLS | Man-in-the-middle | Certificate verification is never disabled; TLS 1.2 minimum; corporate CAs are added to, not substituted for, the system roots |
| Sign-in callback | Authorization code interception | Browser flow uses PKCE (S256) and a random `state` checked in constant time on a `127.0.0.1` loopback listener |

**Out of scope:** a compromised user account or machine (anything running as you can read your own credential files), and the security of IAM Identity Center itself.

## What is written where

| File | Written by | Contains secrets | Permissions |
|---|---|---|---|
| `~/.aws/sso/cache/<sha1>.json` | `login`, silent refresh | Access and refresh token | 0600, atomic; same format as the AWS CLI v2 |
| `~/.aws/sso/cache/credorbit-client-<sha1>.json` | `login` | OIDC client ID and secret (public client registration) | 0600, atomic |
| `~/.aws/credentials` | `export`, `setup` (only when export is enabled) | Temporary keys and `x_credorbit_expiration` | 0600, atomic, locked |
| `~/.aws/config` | `sync`, `setup`, `login --start-url …`, approved role updates | No | Existing mode kept (0600 for a new file), atomic, locked |
| `~/.claude-aws-account` | `rotate`, `setup` | No (a section name) | 0600, atomic |
| `<config dir>/config.yaml` | `config set`, `groups`, `roles set`, `import`, TUI | No | 0600, atomic |
| `<config dir>/state.json` | most commands | No (discovery cache, section names, history) | 0600, atomic |
| `<config dir>/credorbit.log` | every run | No (redacted) | 0600, rotated at 5 MB |
| `<file>.credorbit-<UTC timestamp>.bak` | before every change to the AWS config or credentials file | Same as the source | 0600 |
| `<file>.lock` | while writing | No | 0600 |
| PEM from `ca bundle --out` | `credorbit ca bundle` | No (public certificates) | 0600 |

On Windows, "0600" means CredOrbit replaces the file's ACL with a protected ACL that grants full control to your user only. `credorbit doctor` checks the credentials file, the AWS config and the token cache and reports files readable by others.

## Token and credential handling

- Tokens and credentials are sent only to AWS endpoints over HTTPS, through the AWS SDK for Go v2 and the OIDC API. No subprocess (AWS CLI, `curl`, Python) is involved.
- Expired tokens are refreshed silently with the refresh token when possible; otherwise you are asked to sign in.
- `credorbit logout` deletes the cached token and, when the token is still valid, asks the portal to end the session.
- Credentials are written only when export is enabled. In Normal mode the default is not to write them: AWS SDKs and the AWS CLI resolve SSO profiles from the token cache directly.
- A run that fetches nothing never writes an empty credentials file, and failed profiles keep their previous section.

## Redaction

Log records, error messages and diagnostics pass through a redacting handler that replaces:

| Pattern | Example |
|---|---|
| AWS access key IDs | `AKIA…`, `ASIA…` |
| Secret-looking key/value pairs | `aws_secret_access_key = …`, `"accessToken": "…"`, `refresh_token`, `client_secret`, `password`, `authorization` |
| Bearer tokens | `Authorization: Bearer …` |
| Long base64 blobs | session tokens |
| PEM blocks | `-----BEGIN … -----END …` |

The values of log attributes named `secret`, `token`, `password`, `credentials` and similar are always replaced with `[REDACTED]`. Proxy URLs are shown with their user information replaced by `***`.

## No telemetry

CredOrbit sends no usage data. Its only network destinations are:

| Destination | When |
|---|---|
| `oidc.<region>.amazonaws.com`, `portal.sso.<region>.amazonaws.com` | Sign-in, discovery, credentials |
| `bedrock-runtime.<region>.amazonaws.com` | Only when Bedrock verification is enabled for rotation |
| `network.probe_url` (default: the SSO portal) | `doctor` and `setup` connectivity probe |
| `api.github.com` | Only when you run `credorbit update --check` |

## TLS and corporate networks

TLS verification is never disabled, and there is no flag to disable it. The HTTP client trusts the operating system roots plus any PEM files from `network.ca_bundle` (or `AWS_CA_BUNDLE`) and `network.extra_cas`. Behind a TLS-intercepting proxy or SASE gateway, add the organisation's root CA instead of turning anything off. See [TROUBLESHOOTING.md](TROUBLESHOOTING.md#tls-certificate-signed-by-unknown-authority).

## Backups and recovery

- Before changing `~/.aws/config` or the credentials file, CredOrbit writes `<file>.credorbit-<UTC timestamp>.bak` next to it and keeps the newest `export.backup_keep` (default 5) per file.
- To roll back, copy the backup over the file:

```sh
ls -t ~/.aws/config.credorbit-*.bak | head -n 1
cp ~/.aws/config.credorbit-20261102T101500.000Z.bak ~/.aws/config
```

- The legacy script is only ever read, never modified, by `credorbit import`.

## Safe defaults

| Behaviour | Default |
|---|---|
| Write credentials file | Off (Normal mode) |
| Persist a different role | Only after review |
| Remove stale profiles | Only after review **and** explicit approval (`--allow-remove` in scripts); `--yes` never approves removals |
| Removal when discovery is incomplete, cached or below 80 % of configured accounts | Blocked |
| Self-update | Never; `update --check` only reports |

## Reporting a vulnerability

1. Open a private report at **https://github.com/PiniPP/credorbit/security/advisories/new**.
2. Include the CredOrbit version (`credorbit version`), your OS, steps to reproduce and the impact.
3. Do not include real credentials, tokens, account IDs or start URLs. Use placeholders such as `111111111111` and `https://d-1234567890.awsapps.com/start`.

Fixes are published together with a GitHub Security Advisory that credits the reporter (unless you prefer otherwise).
