package config

import (
	"reflect"
	"testing"
)

func TestDiff(t *testing.T) {
	parse := func(doc string) *Config {
		t.Helper()
		c, err := Parse([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	old := parse(`
sender:
  enabled: true
  sessions:
    - {name: keep, target: 192.0.2.1, schedule: {frequency: 60s}}
    - {name: change, target: 192.0.2.2, schedule: {frequency: 60s}}
    - {name: drop, target: 192.0.2.3, schedule: {frequency: 60s}}
    - {name: inherit, target: 192.0.2.4, schedule: {frequency: 60s}}
`)
	same := Diff(old, old)
	if len(same.SessionsUnchanged) != 4 || same.SessionsAdded != nil || same.SessionsChanged != nil || same.Applied != nil || same.Restart != nil {
		t.Errorf("Diff(old, old) = %+v", same)
	}

	// The defaults change "inherit" through inheritance; "keep" sets its
	// own padding and stays the same.
	updated := parse(`
daemon:
  log: {level: debug}
  metrics: {listen: "127.0.0.1:9999"}
sender:
  enabled: true
  defaults: {padding: 64}
  sessions:
    - {name: keep, target: 192.0.2.1, padding: 27, schedule: {frequency: 60s}}
    - {name: change, target: 192.0.2.22, schedule: {frequency: 60s}}
    - {name: inherit, target: 192.0.2.4, schedule: {frequency: 60s}}
    - {name: new, target: 192.0.2.5, schedule: {frequency: 60s}}
reflector:
  ttl: 64
`)
	want := Changes{
		SessionsAdded:     []string{"new"},
		SessionsRemoved:   []string{"drop"},
		SessionsChanged:   []string{"change", "inherit"},
		SessionsUnchanged: []string{"keep"},
		Applied:           []string{"daemon.log.level", "sender.defaults"},
		Restart:           []string{"daemon.metrics", "reflector"},
	}
	if got := Diff(old, updated); !reflect.DeepEqual(got, want) {
		t.Errorf("Diff = %+v\nwant %+v", got, want)
	}
}

func TestValidateSessionAndTest(t *testing.T) {
	s := Session{Name: "Bad", TestParams: Default().Sender.Defaults, Schedule: Schedule{Frequency: 60e9}}
	err := ValidateSession(&s)
	if err == nil || err.Error() != "invalid configuration (2 problems):\n  session.name: \"Bad\" does not match ^[a-z0-9][a-z0-9_-]{0,62}$\n  session.target: is required" {
		t.Errorf("ValidateSession = %v", err)
	}
	s.Name = "ok"
	if err := ValidateSession(&s); err == nil || err.Error() != "invalid configuration (1 problem):\n  session.target: is required" {
		t.Errorf("ValidateSession(one problem) = %v", err)
	}
	s.Target = "192.0.2.1"
	if err := ValidateSession(&s); err != nil {
		t.Errorf("ValidateSession(valid) = %v", err)
	}
	p := Default().Sender.Defaults
	if err := ValidateTest("192.0.2.1", "", "", &p); err != nil {
		t.Errorf("ValidateTest(valid) = %v", err)
	}
	p.Count = 0
	if err := ValidateTest("", "7", "x", &p); err == nil || len(err.(*Error).Problems) != 4 {
		t.Errorf("ValidateTest(invalid) = %v", err)
	}
}
