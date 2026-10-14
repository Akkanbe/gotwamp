package buildinfo

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func TestComplete(t *testing.T) {
	read := func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v0.9.0"}, Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "f00"}}}, true
	}
	if got := Complete(Info{}, read); got != (Info{Version: "v0.9.0", Commit: "f00"}) {
		t.Errorf("from the module = %+v", got)
	}
	linked := Info{Version: "v1.0.0", Commit: "abc"}
	if got := Complete(linked, read); got != linked {
		t.Errorf("linked values = %+v", got)
	}
	devel := func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true }
	if got := Complete(Info{}, devel); got != (Info{}) {
		t.Errorf("(devel) = %+v", got)
	}
	if got := Complete(linked, func() (*debug.BuildInfo, bool) { return nil, false }); got != linked {
		t.Errorf("no build info = %+v", got)
	}
}

func TestKnownAndString(t *testing.T) {
	if v, c := (Info{}).Known(); v != "dev" || c != "unknown" {
		t.Errorf("Known() = %q, %q", v, c)
	}
	if got, want := (Info{Version: "v1", Commit: "abc"}).String(), "v1 (commit abc, "+runtime.Version()+")"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
