package rotate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ShellIntegration returns a snippet for the user's shell startup file that
// exports AWS_PROFILE from the rotation file on every new shell. Supported
// shells: zsh, bash, fish, powershell (alias pwsh).
func ShellIntegration(shell, file string) (string, error) {
	if strings.TrimSpace(file) == "" {
		return "", fmt.Errorf("rotation file path is empty")
	}
	const header = "# CredOrbit: use the AWS account selected by `credorbit rotate` (Claude Code / Bedrock)\n"
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "zsh", "bash", "sh":
		q := posixQuote(file)
		return header + "# Add to ~/." + rcName(shell) + "\n" +
			"[ -f " + q + " ] && export AWS_PROFILE=\"$(cat " + q + ")\"\n", nil
	case "fish":
		q := fishQuote(file)
		return header + "# Add to ~/.config/fish/config.fish\n" +
			"test -f " + q + "; and set -gx AWS_PROFILE (string trim < " + q + ")\n", nil
	case "powershell", "pwsh":
		q := psQuote(file)
		return header + "# Add to your PowerShell profile ($PROFILE)\n" +
			"if (Test-Path " + q + ") { $env:AWS_PROFILE = (Get-Content " + q + " -Raw).Trim() }\n", nil
	}
	return "", fmt.Errorf("unsupported shell %q (want zsh|bash|fish|powershell)", shell)
}

func rcName(shell string) string {
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "zsh":
		return "zshrc"
	case "bash":
		return "bashrc"
	}
	return "profile"
}

// homeRel splits file into "$HOME" + rest when it lives under the home dir so
// snippets stay portable across machines with different home paths.
func homeRel(file string) (rest string, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	rel, err := filepath.Rel(home, file)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func posixQuote(file string) string {
	if rest, ok := homeRel(file); ok && !strings.ContainsAny(rest, "\"\\$`") {
		return `"$HOME/` + rest + `"`
	}
	return "'" + strings.ReplaceAll(file, "'", `'\''`) + "'"
}

func fishQuote(file string) string {
	if rest, ok := homeRel(file); ok && !strings.ContainsAny(rest, "\"\\$") {
		return `"$HOME/` + rest + `"`
	}
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(file) + "'"
}

func psQuote(file string) string {
	return "'" + strings.ReplaceAll(file, "'", "''") + "'"
}
