// Package buildinfo identifies the gotwamp binaries: the version and commit
// the linker sets (-X main.version, -X main.commit), completed from the
// module build information.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Info is the version and commit of a binary; either may be unset.
type Info struct {
	Version, Commit string
}

// Complete fills in what the linker left unset from read, which is
// debug.ReadBuildInfo outside tests: the module version, unless "(devel)",
// and the VCS revision.
func Complete(i Info, read func() (*debug.BuildInfo, bool)) Info {
	bi, ok := read()
	if !ok {
		return i
	}
	if i.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		i.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		if i.Commit == "" && s.Key == "vcs.revision" {
			i.Commit = s.Value
		}
	}
	return i
}

// Known returns the version and commit, "dev" and "unknown" when unset, as
// every output shows them.
func (i Info) Known() (version, commit string) {
	version, commit = i.Version, i.Commit
	if version == "" {
		version = "dev"
	}
	if commit == "" {
		commit = "unknown"
	}
	return version, commit
}

// String formats the version line: "v1.2.3 (commit abc1234, go1.26.0)".
func (i Info) String() string {
	v, c := i.Known()
	return fmt.Sprintf("%s (commit %s, %s)", v, c, runtime.Version())
}
