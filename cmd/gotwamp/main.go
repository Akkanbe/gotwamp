// Command gotwamp is the command line client of gotwampd. It talks to the
// daemon over gRPC on a Unix domain socket (SPEC §10). The command itself
// is internal/cli.
package main

import (
	"os"

	"github.com/Akkanbe/gotwamp/internal/cli"
)

// Set at link time: -ldflags "-X main.version=... -X main.commit=...".
var (
	version = ""
	commit  = ""
)

// exit is os.Exit, replaced in tests.
var exit = os.Exit

func main() {
	exit(cli.Run(os.Args[1:], cli.BuildInfo{Version: version, Commit: commit}))
}
