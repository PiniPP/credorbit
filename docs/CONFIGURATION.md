<h1><img src="../assets/brand/logo-symbol.svg" width="36" height="36" alt="" align="top"> Configuration</h1>

Every `config.yaml` setting with its default, the environment variables that override it, and where CredOrbit keeps its files.

[← Back to README](../README.md) · [Docs index](README.md)

---

## Where settings live

| File | Default location | Contents |
|---|---|---|
| `config.yaml` | `~/.config/credorbit/config.yaml` (macOS, Linux; `$XDG_CONFIG_HOME/credorbit/` when set) · `%AppData%\CredOrbit\config.yaml` (Windows) | Your settings, groups, preferred roles. **No secrets** |
| `state.json` | same directory | Discovery cache, credential sections CredOrbit manages, last-used roles, rotation history. **No secrets** |
| `credorbit.log` | same directory | JSON log with secrets redacted; rotated to `credorbit.log.1` above 5 MB |

Change the directory with `--config DIR` (one run) or `CREDORBIT_CONFIG_DIR` (environment). Print the active file with:

```sh
credorbit config path
```

## Editing settings

```sh
credorbit config list               # every key, value and description
credorbit config list --json        # same, with the allowed choices
credorbit config get roles.policy
credorbit config set roles.policy strongest
```

`config set` validates the value and writes the file atomically (mode 0600). You can also edit `config.yaml` by hand; unknown keys are logged and reported by `credorbit doctor`, and a file written by a newer CredOrbit version is refused rather than misread. The Settings screen of the terminal UI edits the same keys.

> [!NOTE]
> Lists such as `roles.fallbacks` and `network.extra_cas` are comma-separated on the command line: `credorbit config set network.extra_cas ~/ca/root.pem,~/ca/issuing.pem`. An empty value clears the list.

## Precedence

For every run, the first source that is set wins:

1. Command-line flag (for example `--parallel`, `--session`, `--policy`)
2. Environment variable (for example `PARALLEL`, `SSO_SESSION`)
3. `config.yaml`
4. Built-in default

Environment overrides apply to that run only and are never written back.

## Settings reference

All keys below can be changed with `credorbit config set`.

### General and SSO

| Key | Default | Values | Description |
|---|---|---|---|
| `mode` | `normal` | `normal`, `compat` | Normal mode uses safe defaults; `compat` reproduces the legacy script. Setting it also changes several other keys (see [Modes](#modes)) |
| `sso.default_session` | *(empty)* | session name | Session used when `--session` is not given. When empty and `~/.aws/config` has exactly one session, that one is used |
| `sso.login_flow` | `browser` | `browser`, `device` | `browser` = authorization code with PKCE on a `127.0.0.1` callback; `device` = device code, no local browser needed |

### Profiles

| Key | Default | Values | Description |
|---|---|---|---|
| `profiles.prefix` | `p_` | text without spaces or brackets | Prefix of the profiles CredOrbit creates and manages. New accounts become `<prefix><account name>` |
| `profiles.region` | *(empty)* | AWS region | `region` written to new profiles; empty = the SSO session's region |
| `profiles.output` | *(empty)* | e.g. `json` | `output` written to new profiles; empty = not written |

### Roles

| Key | Default | Values | Description |
|---|---|---|---|
| `roles.policy` | `configured` | `configured`, `manual`, `strongest`, `readonly`, `script` | How the role is chosen at export time (see [Role policies](#role-policies)) |
| `roles.fallbacks` | *(empty)* | comma-separated roles | Roles tried last, **`script` policy only** |
| `roles.update_config` | `false` | `true`, `false` | Persist the role that worked to `~/.aws/config` without asking |

### Sync

| Key | Default | Values | Description |
|---|---|---|---|
| `sync.add_new` | `true` | `true`, `false` | Add profiles for newly discovered accounts |
| `sync.remove_stale` | `prompt` | `prompt`, `never` | `prompt` = stale removals are offered for review; `never` = never proposed |
| `sync.threshold` | `0.8` | number in (0, 1] | Mass-removal safeguard: if fewer than `threshold × configured` accounts are discovered, nothing is removed |

### Export

| Key | Default | Values | Description |
|---|---|---|---|
| `export.enabled` | `false` | `true`, `false` | Write temporary credentials to the credentials file. When `false`, `export` only verifies access and your profiles keep working through the SSO token cache |
| `export.credentials_file` | *(empty)* | path | Credentials file; empty = `AWS_SHARED_CREDENTIALS_FILE` or `~/.aws/credentials` |
| `export.strip_prefix` | `true` | `true`, `false` | Write profile `p_x` as section `[x]` (legacy layout) |
| `export.default_profile` | *(empty)* | profile name | Profile whose credentials are copied to `[default]` (not in single-profile runs) |
| `export.concurrency` | `10` | 1–64 | Parallel `GetRoleCredentials` requests |
| `export.backup_keep` | `5` | ≥ 0 | Timestamped backups kept per file (`0` is read back as the default, 5) |
| `export.include_new_in_main` | `false` | `true`, `false` | Include newly discovered accounts in Main exports. Takes effect in Script-compatible mode |

### Claude / Bedrock rotation

| Key | Default | Values | Description |
|---|---|---|---|
| `rotation.strategy` | `random` | `random`, `round-robin`, `manual` | How `credorbit rotate` picks the next account. `manual` requires `--set` |
| `rotation.verify_bedrock` | `false` | `true`, `false` | Send a one-token Bedrock `Converse` call before switching |
| `rotation.bedrock_region` | *(empty)* | AWS region | Region for the verification call (required when verifying) |
| `rotation.bedrock_model` | *(empty)* | model or inference-profile ID | Model for the verification call (required when verifying) |
| `rotation.file` | *(empty)* | path | File your shell reads; empty = `~/.claude-aws-account` |

### Network

| Key | Default | Values | Description |
|---|---|---|---|
| `network.ca_bundle` | *(empty)* | PEM file | Bundle appended to the system roots. When empty, `AWS_CA_BUNDLE` is used instead |
| `network.extra_cas` | *(empty)* | comma-separated PEM files | More PEM files appended to the system roots. Ignored for one run with `--no-extra-ca` |
| `network.probe_url` | *(empty)* | https URL | URL probed by `doctor` and `setup`; empty = `https://portal.sso.<region>.amazonaws.com/` |

A missing or unreadable CA file is an error that names the file, rather than a confusing "unknown authority" later.

### UI and updates

| Key | Default | Values | Description |
|---|---|---|---|
| `ui.theme` | `default` | `default`, `light`, `mono` | Terminal UI theme. `NO_COLOR` forces `mono` |
| `ui.ascii` | `false` | `true`, `false` | ASCII-only rendering (no Unicode glyphs or box drawing) |
| `update.repo` | `PiniPP/credorbit` | `owner/name` | GitHub repository checked by `credorbit update --check` |
| `update.check` | `true` | `true`, `false` | Allow update checks |

## Keys edited by other commands

These live in `config.yaml` but are not part of `config set`:

| Key | Edit with | Description |
|---|---|---|
| `groups.<name>.members` | `credorbit groups …`, TUI group editor | Group members: `account_id`, optional `profile`, optional `role` |
| `groups.<name>.description` | `groups create --description` | Free text |
| `accounts.<account-id>.role` | `credorbit roles set` | Preferred role for an account |
| `rotation.accounts` | `credorbit rotate accounts set\|add\|remove` | Credentials sections eligible for rotation |
| `rotation.target` | hand edit | Rotation target; only `claude` is supported |
| `roles.ranking` | hand edit | Custom role ranking rules (see below) |
| `network.combined_bundle_path` | hand edit | Path of a combined PEM bundle that `doctor` checks for |
| `version` | automatic | Schema version (currently `1`) |

## Example `config.yaml`

<details>
<summary><b>Show a complete example</b></summary>

```yaml
# CredOrbit settings — no secrets are stored here
version: 1
mode: normal
sso:
  default_session: acme
  login_flow: browser
profiles:
  prefix: p_
  region: eu-west-1
  output: json
groups:
  main:
    description: Accounts exported in main mode
    members:
      - account_id: "111111111111"
        profile: p_acme-billing-prd
      - account_id: "222222222222"
        profile: p_acme-ledger-dev
  dev:
    description: Development accounts
    members:
      - account_id: "222222222222"
accounts:
  "111111111111":
    role: ReadOnlyAccess
roles:
  policy: configured
  fallbacks: []
  ranking: []
  update_config: false
sync:
  add_new: true
  remove_stale: prompt
  threshold: 0.8
export:
  enabled: true
  credentials_file: ""
  strip_prefix: true
  default_profile: p_acme-ledger-dev
  concurrency: 10
  backup_keep: 5
  include_new_in_main: false
rotation:
  target: claude
  file: ""
  accounts: [acme-bedrock-01, acme-bedrock-02]
  strategy: random
  verify_bedrock: true
  bedrock_region: us-east-1
  bedrock_model: anthropic.claude-example-model-id
network:
  ca_bundle: ""
  extra_cas: [~/certs/acme-root-ca.pem]
  probe_url: ""
  combined_bundle_path: ~/.aws/combined-ca.pem
ui:
  theme: default
  ascii: false
update:
  repo: PiniPP/credorbit
  check: true
```

</details>

## Modes

`credorbit config set mode compat` (or `credorbit import --compat`) switches to Script-compatible mode and sets:

| Key | Value |
|---|---|
| `roles.policy` | `script` |
| `roles.update_config` | `true` |
| `export.enabled` | `true` |
| `export.strip_prefix` | `true` |
| `export.include_new_in_main` | `true` |
| `sync.add_new` | `true` |
| `sync.remove_stale` | `prompt` (removals stay reviewed) |
| `rotation.strategy` | `random` |

`credorbit config set mode normal` restores `roles.policy=configured`, `roles.update_config=false` and `export.enabled=false`; the other keys keep their values. `--compat` applies Script-compatible behaviour to a single run without saving anything. See [MIGRATION.md](MIGRATION.md).

## Role policies

| Policy | Role tried for each profile |
|---|---|
| `configured` | The profile's `sso_role_name`, or the preferred role from `credorbit roles set`. Skipped if discovery shows it is no longer assigned |
| `manual` | The preferred role chosen per account (`credorbit roles set`, TUI role picker) |
| `strongest` | Assigned roles, strongest first |
| `readonly` | Assigned roles, least privileged first |
| `script` | Assigned roles strongest first, then the configured role, then `roles.fallbacks` (legacy behaviour) |

<details>
<summary><b>Default role ranking and custom rules</b></summary>

Lower weight = stronger. Matching is a case-insensitive glob; the first matching rule wins; unmatched roles weigh 400; ties sort alphabetically.

| Match | Weight |
|---|---|
| `administratoraccess` | 0 |
| `*admin*-dr` | 200 |
| `*admin*` | 100 |
| `*poweruser*` | 300 |
| `*devops*` | 300 |
| `*readonly*` | 500 |
| `*viewonly*` | 500 |

Replace the ranking in `config.yaml` (an empty list means the default above):

```yaml
roles:
  ranking:
    - match: "*platform-admin*"
      weight: 10
    - match: "*readonly*"
      weight: 500
```

</details>

## Environment variables

| Variable | Effect | Overridden by |
|---|---|---|
| `SSO_SESSION` | Session used when `--session` is not given (beats `sso.default_session`) | `--session` |
| `FORCE_LOGIN` | `1`/`true`/`yes`: sign in again during `setup` even if the token is valid | `--force-login` |
| `PARALLEL` | Parallel credential requests (capped at 64), beats `export.concurrency` | `--parallel` |
| `CREDS_FILE` | Credentials file, beats `export.credentials_file` and `AWS_SHARED_CREDENTIALS_FILE` | — |
| `SYNC` | `0`/`false`/`no`: skip the `~/.aws/config` sync step of `setup` | `--no-sync` |
| `ROLE_UPDATE` | `1`/`true`/`yes` persists, any other value disables persisting the role that worked (beats `roles.update_config` and the mode) | `--update-roles`, `--no-update-roles` |
| `FALLBACK_ROLES` | Space-separated fallback roles for the `script` policy, beats `roles.fallbacks` | `--fallback-role` |
| `AWS_CA_BUNDLE` | PEM bundle appended to the system roots when `network.ca_bundle` is empty | `network.ca_bundle` |
| `AWS_CONFIG_FILE` | Location of the AWS config file (default `~/.aws/config`) | — |
| `AWS_SHARED_CREDENTIALS_FILE` | Location of the credentials file (default `~/.aws/credentials`) | `CREDS_FILE`, `export.credentials_file` |
| `CREDORBIT_CONFIG_DIR` | CredOrbit's settings directory | `--config` |
| `XDG_CONFIG_HOME` | Base of the default settings directory on macOS and Linux | `CREDORBIT_CONFIG_DIR` |
| `NO_COLOR` | Disable colours; the terminal UI uses the `mono` theme. `--no-color` sets it | — |
| `CREDORBIT_ASCII` | `1` or `true`: ASCII-only rendering. `--ascii` sets it | — |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | Standard proxy settings for all HTTPS calls (credentials in the URL are redacted in output) | — |
| `AWS_PROFILE` | Read by `doctor` to warn when it differs from the rotation file | — |

> [!TIP]
> The terminal UI also switches to ASCII when the locale (`LC_ALL`, `LC_CTYPE`, `LANG`) is not UTF-8. Windows Terminal (`WT_SESSION`) is treated as UTF-8.

## File locations

| File | Default | Override |
|---|---|---|
| AWS config | `~/.aws/config` | `AWS_CONFIG_FILE` |
| Credentials | `~/.aws/credentials` | `CREDS_FILE`, `export.credentials_file`, `AWS_SHARED_CREDENTIALS_FILE` |
| SSO token cache | `~/.aws/sso/cache/` (AWS CLI v2 format) | — |
| Rotation file | `~/.claude-aws-account` | `rotation.file` |
| Legacy script (import source) | `~/populate-aws-creds.sh` | `credorbit import --script PATH` |
| Settings directory | `~/.config/credorbit/` or `%AppData%\CredOrbit\` | `CREDORBIT_CONFIG_DIR`, `--config` |
