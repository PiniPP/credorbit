<h1><img src="../assets/brand/logo-symbol.svg" width="36" height="36" alt="" align="top"> Installation</h1>

Install CredOrbit on macOS, Linux or Windows, add shell completion, verify downloads and uninstall cleanly.

[← Back to README](../README.md) · [Docs index](README.md)

---

> [!IMPORTANT]
> Use inside a company or enterprise requires a paid commercial license — see [COMMERCIAL.md](../COMMERCIAL.md).

## Requirements

| | |
|---|---|
| Operating systems | macOS, Linux, Windows (amd64 and arm64) |
| Runtime dependencies | None. One static binary; the AWS CLI is **not** required |
| Network | HTTPS to IAM Identity Center (`portal.sso.<region>.amazonaws.com`, `oidc.<region>.amazonaws.com`) |
| Building from source | Go (version in [`go.mod`](../go.mod)) and POSIX `make` |

## Choose a method

| Method | Best for | Verifies checksum |
|---|---|---|
| [Install script (macOS / Linux)](#macos-and-linux-install-script) | Most users | Yes |
| [Install script (Windows)](#windows-install-script) | Most Windows users | Yes |
| [Release archive](#release-archives) | Air-gapped or managed machines | Manually |
| [`go install`](#go-install) | Go developers | Go module checksum database |
| [Build from source](#build-from-source) | Contributors | n/a |
| Homebrew | Tap coming with the first release | n/a |

## macOS and Linux install script

```sh
curl -fsSL https://raw.githubusercontent.com/PiniPP/credorbit/main/packaging/install.sh | sh
```

The script detects your OS and architecture, downloads the release archive and `checksums.txt`, checks the SHA-256, and installs `credorbit` to `~/.local/bin`. It never uses `sudo`.

| Variable | Default | Purpose |
|---|---|---|
| `CREDORBIT_VERSION` | latest release | Tag to install, e.g. `v1.2.3` |
| `CREDORBIT_INSTALL` | `~/.local/bin` | Install directory (must be writable) |
| `CREDORBIT_REPO` | `PiniPP/credorbit` | Repository to download from (forks, mirrors) |
| `GITHUB_TOKEN` | unset | Optional; raises GitHub API rate limits |

```sh
# pin a version and install somewhere else
curl -fsSL https://raw.githubusercontent.com/PiniPP/credorbit/main/packaging/install.sh \
  | CREDORBIT_VERSION=v1.2.3 CREDORBIT_INSTALL="$HOME/bin" sh
```

> [!TIP]
> Prefer to read before you run? Download `install.sh`, inspect it, then run `sh install.sh`.

## Windows install script

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/PiniPP/credorbit/main/packaging/install.ps1 -OutFile install.ps1
powershell -ExecutionPolicy Bypass -File install.ps1 -AddToPath
```

`credorbit.exe` is installed to `%LOCALAPPDATA%\Programs\CredOrbit`. No administrator rights are needed. Your user `PATH` changes only with `-AddToPath`.

| Parameter | Default | Purpose |
|---|---|---|
| `-Version` | `$env:CREDORBIT_VERSION`, else latest | Tag to install |
| `-Repo` | `$env:CREDORBIT_REPO`, else `PiniPP/credorbit` | Repository |
| `-InstallDir` | `%LOCALAPPDATA%\Programs\CredOrbit` | Target directory |
| `-AddToPath` | off | Add the install directory to your user `PATH` |

Open a new terminal after `-AddToPath` so the change is picked up.

## Release archives

Every [release](https://github.com/PiniPP/credorbit/releases) contains:

| File | Contents |
|---|---|
| `credorbit_<version>_<os>_<arch>.tar.gz` | macOS and Linux binary, README, LICENSE, CHANGELOG, docs, completions |
| `credorbit_<version>_windows_<arch>.zip` | Windows binary and the same extras |
| `*.deb`, `*.rpm` | Linux packages (binary in `/usr/bin`, completions for bash, zsh, fish) |
| `checksums.txt` | SHA-256 of every artifact |

`<version>` has no leading `v` (for example `credorbit_1.2.3_linux_amd64.tar.gz`).

### Verify checksums

```sh
# Linux
sha256sum --check --ignore-missing checksums.txt
# macOS
shasum -a 256 --check --ignore-missing checksums.txt
```

```powershell
# Windows: compare with the matching line in checksums.txt
(Get-FileHash .\credorbit_1.2.3_windows_amd64.zip -Algorithm SHA256).Hash.ToLower()
```

> [!WARNING]
> If a checksum does not match, do not run the binary. Download it again from the releases page.

Then extract the archive and move `credorbit` (or `credorbit.exe`) to a directory on your `PATH`.

## go install

```sh
go install github.com/PiniPP/credorbit/cmd/credorbit@latest
```

The binary lands in `$(go env GOBIN)`, or `$(go env GOPATH)/bin` when `GOBIN` is unset.

## Build from source

```sh
git clone https://github.com/PiniPP/credorbit.git
cd credorbit
make build      # ./credorbit for this platform
make install    # copies it to $GOBIN, or ~/bin when GOBIN is unset
```

`make cross` builds every release platform into `dist/`. Run `make help` for all targets.

## Put it on your PATH

| Shell | Add to | Line |
|---|---|---|
| zsh | `~/.zshrc` | `export PATH="$HOME/.local/bin:$PATH"` |
| bash | `~/.bashrc` | `export PATH="$HOME/.local/bin:$PATH"` |
| fish | `~/.config/fish/config.fish` | `fish_add_path ~/.local/bin` |
| PowerShell | run once | re-run the installer with `-AddToPath` |

Check the result:

```sh
credorbit version
```

## Shell completion

Completion scripts are generated by `credorbit completion <shell>`. Release archives also ship them in `completions/`, and the Linux packages install them automatically.

<details>
<summary><b>zsh</b></summary>

```sh
# once, if completion is not enabled yet
echo "autoload -U compinit; compinit" >> ~/.zshrc
# install the completion
credorbit completion zsh > "${fpath[1]}/_credorbit"
```

With Oh My Zsh or a read-only `fpath[1]`, write to a directory you own and add it to `fpath` before `compinit`.

</details>

<details>
<summary><b>bash</b></summary>

Requires the `bash-completion` package.

```sh
# current shell
source <(credorbit completion bash)
# permanently (Linux)
credorbit completion bash | sudo tee /etc/bash_completion.d/credorbit > /dev/null
# permanently (macOS with Homebrew bash-completion@2)
credorbit completion bash > "$(brew --prefix)/etc/bash_completion.d/credorbit"
```

</details>

<details>
<summary><b>fish</b></summary>

```sh
credorbit completion fish > ~/.config/fish/completions/credorbit.fish
```

</details>

<details>
<summary><b>PowerShell</b></summary>

```powershell
# current session
credorbit completion powershell | Out-String | Invoke-Expression
# permanently: add the same line to your profile
notepad $PROFILE
```

</details>

## First run

```sh
credorbit doctor   # checks AWS config, SSO, network/TLS and file permissions
credorbit          # opens the terminal UI
```

Continue with the [User Guide](USER_GUIDE.md).

## Updating

CredOrbit never updates itself. Check for a newer release with:

```sh
credorbit update --check
```

Then re-run the install method you used.

## Uninstall

1. Optionally sign out, which deletes the cached SSO token: `credorbit logout`.
2. Remove the binary (`~/.local/bin/credorbit`, `$GOBIN/credorbit`, or `%LOCALAPPDATA%\Programs\CredOrbit\credorbit.exe`).
3. Optionally remove CredOrbit's own files:

| What | macOS / Linux | Windows |
|---|---|---|
| Settings, state, log | `~/.config/credorbit/` (or `$XDG_CONFIG_HOME/credorbit/`) | `%AppData%\CredOrbit\` |
| Rotation file | `~/.claude-aws-account` | `%USERPROFILE%\.claude-aws-account` |
| Backups | `~/.aws/config.credorbit-*.bak`, `~/.aws/credentials.credorbit-*.bak` | same names under `%USERPROFILE%\.aws\` |
| OIDC client registration | `~/.aws/sso/cache/credorbit-client-*.json` | same under `%USERPROFILE%\.aws\sso\cache\` |

> [!NOTE]
> Uninstalling leaves `~/.aws/config` and `~/.aws/credentials` as they are. The profiles CredOrbit created are ordinary AWS CLI profiles and keep working with the AWS CLI and SDKs.
