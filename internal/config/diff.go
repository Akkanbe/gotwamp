package config

import (
	"reflect"
	"slices"
)

// Changes is how a new configuration differs from the running one (SPEC
// §9.3 reload). Session lists hold session names, sorted.
type Changes struct {
	SessionsAdded, SessionsRemoved, SessionsChanged, SessionsUnchanged []string
	// Applied lists the settings that changed and take effect at once:
	// daemon.log.level and sender.defaults (for later ad-hoc tests; the
	// sessions show their effect as changes).
	Applied []string
	// Restart lists the settings that changed but take effect only after a
	// restart, because they bind sockets or open files.
	Restart []string
}

// Diff compares the running configuration cur with next.
func Diff(cur, next *Config) Changes {
	var ch Changes
	prev := make(map[string]Session, len(cur.Sender.Sessions))
	for _, s := range cur.Sender.Sessions {
		prev[s.Name] = s
	}
	for _, s := range next.Sender.Sessions {
		p, ok := prev[s.Name]
		switch {
		case !ok:
			ch.SessionsAdded = append(ch.SessionsAdded, s.Name)
		case reflect.DeepEqual(p, s):
			ch.SessionsUnchanged = append(ch.SessionsUnchanged, s.Name)
		default:
			ch.SessionsChanged = append(ch.SessionsChanged, s.Name)
		}
		delete(prev, s.Name)
	}
	for name := range prev {
		ch.SessionsRemoved = append(ch.SessionsRemoved, name)
	}
	for _, l := range [][]string{ch.SessionsAdded, ch.SessionsRemoved, ch.SessionsChanged, ch.SessionsUnchanged} {
		slices.Sort(l)
	}

	applied := []struct {
		key      string
		was, now any
	}{
		{"daemon.log.level", cur.Daemon.Log.Level, next.Daemon.Log.Level},
		{"sender.defaults", cur.Sender.Defaults, next.Sender.Defaults},
	}
	for _, s := range applied {
		if !reflect.DeepEqual(s.was, s.now) {
			ch.Applied = append(ch.Applied, s.key)
		}
	}
	restart := []struct {
		key      string
		was, now any
	}{
		{"daemon.api", cur.Daemon.API, next.Daemon.API},
		{"daemon.metrics", cur.Daemon.Metrics, next.Daemon.Metrics},
		{"daemon.log.format", cur.Daemon.Log.Format, next.Daemon.Log.Format},
		{"daemon.timestamping", cur.Daemon.Timestamping, next.Daemon.Timestamping},
		{"daemon.hardware_interface", cur.Daemon.HardwareInterface, next.Daemon.HardwareInterface},
		{"daemon.clock", cur.Daemon.Clock, next.Daemon.Clock},
		{"daemon.realtime", cur.Daemon.Realtime, next.Daemon.Realtime},
		{"daemon.limits", cur.Daemon.Limits, next.Daemon.Limits},
		{"sender.enabled", cur.Sender.Enabled, next.Sender.Enabled},
		{"sender.keys_file", cur.Sender.KeysFile, next.Sender.KeysFile},
		{"reflector", cur.Reflector, next.Reflector},
		{"export", cur.Export, next.Export},
		{"store", cur.Store, next.Store},
	}
	for _, s := range restart {
		if !reflect.DeepEqual(s.was, s.now) {
			ch.Restart = append(ch.Restart, s.key)
		}
	}
	return ch
}
