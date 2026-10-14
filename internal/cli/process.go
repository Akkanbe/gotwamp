package cli

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/Akkanbe/gotwamp/internal/buildinfo"
)

// Run is the gotwamp process: Main with the standard streams, SIGINT and
// SIGTERM, and info completed from the module build information. It
// returns the exit status.
func Run(args []string, info BuildInfo) int {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	interrupts := make(chan struct{}, 2)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-sigs:
				select {
				case interrupts <- struct{}{}:
				default: // two are enough: the second cancels everything
				}
			case <-done:
				return
			}
		}
	}()
	return Main(context.Background(), args, buildinfo.Complete(info, debug.ReadBuildInfo),
		Env{Stdout: os.Stdout, Stderr: os.Stderr, Interrupt: interrupts})
}
