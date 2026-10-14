package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"gopkg.in/yaml.v3"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
)

// sessionFile is one session in YAML, with the schema of sender.sessions[].
// Pointers tell keys left out, which gotwampd fills from sender.defaults.
type sessionFile struct {
	Name                   string         `yaml:"name"`
	Target                 string         `yaml:"target"`
	IPV                    string         `yaml:"ipv"`
	Source                 string         `yaml:"source"`
	Mode                   *string        `yaml:"mode"`
	ControlPort            *uint32        `yaml:"control_port"`
	ReceiverPort           *uint32        `yaml:"receiver_port"`
	SenderPort             *uint32        `yaml:"sender_port"`
	SenderPortRange        *string        `yaml:"sender_port_range"`
	Count                  *uint32        `yaml:"count"`
	Interval               *time.Duration `yaml:"interval"`
	Timeout                *time.Duration `yaml:"timeout"`
	Padding                *uint32        `yaml:"padding"`
	PaddingFill            *string        `yaml:"padding_fill"`
	DSCP                   *config.DSCP   `yaml:"dscp"`
	TTL                    *uint32        `yaml:"ttl"`
	ControlTimeout         *time.Duration `yaml:"control_timeout"`
	ControlDSCP            *config.DSCP   `yaml:"control_dscp"`
	EndDelay               *time.Duration `yaml:"end_delay"`
	ZeroAddresses          *bool          `yaml:"zero_addresses"`
	StartTime              *string        `yaml:"start_time"`
	AcceptAlternatePort    *bool          `yaml:"accept_alternate_port"`
	TolerateZeroMultiplier *bool          `yaml:"tolerate_zero_multiplier"`
	ReflectorSequence      *string        `yaml:"reflector_sequence"`
	DSCPECNMonitoring      *bool          `yaml:"dscp_ecn_monitoring"`
	Security               *string        `yaml:"security"`
	KeyID                  *string        `yaml:"key_id"`
	Extensions             []string       `yaml:"extensions"`
	OctetsToReflect        *uint32        `yaml:"octets_to_reflect"`
	PaddingToReflect       *uint32        `yaml:"padding_to_reflect"`
	STAMP                  *bool          `yaml:"stamp"`
	SSID                   *uint32        `yaml:"ssid"`
	// TxTimestampWait is a key of sender.sessions[] that the API does not
	// take; it is read only to say so.
	TxTimestampWait *time.Duration    `yaml:"tx_timestamp_wait"`
	Labels          map[string]string `yaml:"labels"`
	Schedule        struct {
		Type      string        `yaml:"type"`
		Frequency time.Duration `yaml:"frequency"`
		Jitter    time.Duration `yaml:"jitter"`
		Window    time.Duration `yaml:"window"`
	} `yaml:"schedule"`
}

// parseSessionFile reads one session. Unknown keys are errors; gotwampd
// validates the values.
//
//declscope:package // session add and update read files with it
func parseSessionFile(b []byte) (*gotwampv1.SessionSpec, error) {
	var f sessionFile
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the file is empty")
		}
		return nil, fmt.Errorf("yaml: %w", err)
	}
	t := &gotwampv1.TestSpec{
		Target:        f.Target,
		SourceAddress: f.Source,
		ReceiverPort:  f.ReceiverPort,
		SenderPort:    f.SenderPort,
		Count:         f.Count,
		Padding:       f.Padding,
	}
	switch f.IPV {
	case "", "auto":
	case "4":
		t.Ipv = gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV4
	case "6":
		t.Ipv = gotwampv1.AddressFamily_ADDRESS_FAMILY_IPV6
	default:
		return nil, fmt.Errorf("ipv %q is not auto, 4 or 6", f.IPV)
	}
	if f.Mode != nil {
		m, ok := enumValue(gotwampv1.Mode_value, "MODE_", *f.Mode)
		if !ok {
			return nil, fmt.Errorf("mode %q is not full or light", *f.Mode)
		}
		t.Mode = gotwampv1.Mode(m)
	}
	if f.ControlPort != nil {
		t.ControlPort = *f.ControlPort
	}
	t.Interval, t.Timeout, t.ControlTimeout = optDuration(f.Interval), optDuration(f.Timeout), optDuration(f.ControlTimeout)
	if f.PaddingFill != nil {
		v, ok := enumValue(gotwampv1.PaddingFill_value, "PADDING_FILL_", *f.PaddingFill)
		if !ok {
			return nil, fmt.Errorf("padding_fill %q is not auto, random or zero", *f.PaddingFill)
		}
		t.PaddingFill = gotwampv1.PaddingFill(v)
	}
	if f.DSCP != nil {
		v := uint32(*f.DSCP)
		t.Dscp = &v
	}
	if f.TTL != nil {
		t.Ttl = *f.TTL
	}
	t.DscpEcnMonitoring = f.DSCPECNMonitoring
	if f.Security != nil {
		v, ok := enumValue(gotwampv1.SecurityMode_value, "SECURITY_MODE_", *f.Security)
		if !ok {
			return nil, fmt.Errorf("security %q is not unauthenticated, authenticated, encrypted or mixed", *f.Security)
		}
		t.Security = gotwampv1.SecurityMode(v)
	}
	if f.KeyID != nil {
		t.KeyId = *f.KeyID
	}
	for _, name := range f.Extensions {
		v, ok := enumValue(gotwampv1.Extension_value, "EXTENSION_", name)
		if !ok {
			return nil, fmt.Errorf("extensions: %q is not individual_session_control, reflect_octets or symmetrical_size", name)
		}
		t.Extensions = append(t.Extensions, gotwampv1.Extension(v))
	}
	t.OctetsToReflect, t.PaddingToReflect, t.Stamp, t.Ssid = f.OctetsToReflect, f.PaddingToReflect, f.STAMP, f.SSID
	if f.TxTimestampWait != nil {
		return nil, errors.New("tx_timestamp_wait cannot be set for a session created through the API; it comes from sender.defaults")
	}
	c := &gotwampv1.CompatOptions{
		ZeroAddresses:          f.ZeroAddresses,
		AcceptAlternatePort:    f.AcceptAlternatePort,
		TolerateZeroMultiplier: f.TolerateZeroMultiplier,
		EndDelay:               optDuration(f.EndDelay),
	}
	if f.ControlDSCP != nil {
		v := uint32(*f.ControlDSCP)
		c.ControlDscp = &v
	}
	if f.StartTime != nil {
		v, ok := enumValue(gotwampv1.StartTime_value, "START_TIME_", *f.StartTime)
		if !ok {
			return nil, fmt.Errorf("start_time %q is not zero or now", *f.StartTime)
		}
		c.StartTime = gotwampv1.StartTime(v)
	}
	if f.ReflectorSequence != nil {
		v, ok := enumValue(gotwampv1.ReflectorSequence_value, "REFLECTOR_SEQUENCE_", *f.ReflectorSequence)
		if !ok {
			return nil, fmt.Errorf("reflector_sequence %q is not stateful or copy_sender", *f.ReflectorSequence)
		}
		c.ReflectorSequence = gotwampv1.ReflectorSequence(v)
	}
	if f.SenderPortRange != nil {
		r, err := config.ParsePortRange(*f.SenderPortRange)
		if err != nil {
			return nil, err
		}
		c.SenderPortRange = &gotwampv1.PortRange{First: uint32(r.First), Last: uint32(r.Last)}
	}
	t.Compat = c

	s := &gotwampv1.Schedule{
		Frequency: durationpb.New(f.Schedule.Frequency),
		Jitter:    durationpb.New(f.Schedule.Jitter),
		Window:    durationpb.New(f.Schedule.Window),
	}
	switch f.Schedule.Type {
	case "", "periodic":
		s.Type = gotwampv1.ScheduleType_SCHEDULE_TYPE_PERIODIC
	case "continuous":
		s.Type = gotwampv1.ScheduleType_SCHEDULE_TYPE_CONTINUOUS
	default:
		return nil, fmt.Errorf("schedule.type %q is not periodic or continuous", f.Schedule.Type)
	}
	return &gotwampv1.SessionSpec{Name: f.Name, Spec: t, Schedule: s, Labels: f.Labels}, nil
}

func optDuration(d *time.Duration) *durationpb.Duration {
	if d == nil {
		return nil
	}
	return durationpb.New(*d)
}

// enumValue looks the configuration name s (such as "copy_sender") up in
// the values of a proto enum whose names begin with prefix. The empty name
// and "unspecified" are not values.
func enumValue(values map[string]int32, prefix, s string) (int32, bool) {
	if s == "" || strings.EqualFold(s, "unspecified") {
		return 0, false
	}
	v, ok := values[prefix+strings.ToUpper(s)]
	return v, ok
}
