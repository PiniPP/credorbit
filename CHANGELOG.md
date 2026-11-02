# Changelog

All notable changes to CredOrbit are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Initial release of CredOrbit, the successor to the legacy `populate-aws-creds.sh` script.

### Added

- **SSO login** to AWS IAM Identity Center with the browser flow (authorization code + PKCE on a loopback callback) or device code; silent refresh; AWS CLI v2 compatible token cache; `login`, `logout`, and session creation with `login --name --start-url --region`.
- **Account discovery** of every assigned account and role, with pagination, retries, bounded concurrency and an offline cache (`discover`, `accounts list`).
- **Profile sync** for `~/.aws/config`: new accounts become `<prefix><name>` profiles; stale profiles are removed only after review, behind the 80 % mass-removal safeguard and never on incomplete or cached discovery (`sync`).
- **Temporary credentials** in parallel with live progress and classified errors (no access, token expired, transient); selective updates of the credentials file that keep failed profiles' previous sections (`export`).
- **Full Setup & Sync** workflow with a single reviewed plan, backups, validation and a final report (`setup`, TUI wizard).
- **Main Accounts and custom groups** as export scopes, stored in `config.yaml` (`groups`).
- **Role policies** `configured`, `manual`, `strongest`, `readonly` and `script`, preferred roles per account and reviewed role updates (`roles`).
- **Claude Code / Bedrock rotation** with random, round-robin and manual strategies, eligibility based on unexpired credentials, optional Bedrock verification and shell integration snippets for zsh, bash, fish and PowerShell (`rotate`).
- **Script-compatible mode** (`mode: compat`, `--compat`) reproducing the legacy script's behaviour, and a one-time, read-only **import** of the legacy script (`import`).
- **Terminal UI** (Bubble Tea) with main menu, setup wizard, account browser, group editor, sync, export, roles, rotation, status, diagnostics and settings screens; demo mode (`ui --demo`); default, light and mono themes; ASCII fallback.
- **Corporate network support**: extra CAs appended to the system roots, TLS-interception detection, combined PEM bundles (`ca bundle`), proxy support from the environment.
- **Diagnostics** (`doctor`), local summary (`status`), validated settings (`config`), update check (`update --check`), `--json` output and documented exit codes.
- **Safe file handling**: advisory locks, re-parse validation, timestamped backups with retention, atomic writes, owner-only permissions (0600 / owner-only ACL on Windows), redacted logs.
- Release tooling: install scripts with checksum verification for macOS, Linux and Windows, GoReleaser archives, Linux packages and shell completions.

[Unreleased]: https://github.com/PiniPP/credorbit/commits/main
