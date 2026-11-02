// Command credorbit — Your Cloud Access. In Orbit.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/PiniPP/credorbit/internal/cli"
	"github.com/PiniPP/credorbit/internal/tui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	cli.RunUI = tui.Run
	code := cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
