package cli

import (
	"errors"
	"net/url"
	"os/exec"
	"runtime"
)

// openBrowser opens an http(s) URL in the default browser. The URL is passed
// as a single argument (never through a shell).
func openBrowser(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("refusing to open non-http(s) URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", raw)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", raw)
	default:
		cmd = exec.Command("xdg-open", raw)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
