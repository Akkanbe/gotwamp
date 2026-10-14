//declscope:namespace sqlite

package sqlite

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Akkanbe/gotwamp/internal/sender"
	"github.com/Akkanbe/gotwamp/internal/store"
)

// This file is the encoding of the results in the database: JSON with Go's
// field names, which a field added later reads as its zero value from older
// rows. The session descriptions (infos) stay JSON that SQLite tools can
// read; the result payloads are compressed (compact.go).

// storedInfo is a store.SessionInfo as the infos table keeps it.
type storedInfo struct {
	Name        string            `json:"name"`
	Target      string            `json:"target"`
	Mode        string            `json:"mode"`
	Labels      map[string]string `json:"labels,omitempty"`
	Count       uint32            `json:"count,omitempty"`
	Interval    time.Duration     `json:"interval"`
	Padding     int               `json:"padding"`
	DSCP        int               `json:"dscp"`
	ControlPort uint16            `json:"control_port,omitempty"`
	Definition  json.RawMessage   `json:"definition,omitempty"`
}

// encodeInfo returns the JSON of info and its digest, which identifies equal
// descriptions. A definition the codec cannot encode is left out.
func encodeInfo(info *store.SessionInfo, codec DefinitionCodec) (data, digest []byte, err error) {
	si := storedInfo{
		Name: info.Name, Target: info.Target, Mode: info.Mode, Labels: info.Labels, Count: info.Count,
		Interval: info.Interval, Padding: info.Padding, DSCP: info.DSCP, ControlPort: info.ControlPort,
	}
	var defErr error
	if info.Definition != nil && codec.Encode != nil {
		def, err := codec.Encode(info.Definition)
		if err == nil && !json.Valid(def) {
			// Keep any octets: as a JSON string.
			def, err = json.Marshal(string(def))
		}
		if err == nil {
			si.Definition = def
		}
		defErr = err
	}
	data, err = json.Marshal(si)
	if err != nil {
		return nil, nil, fmt.Errorf("encode session %q: %w", info.Name, err)
	}
	sum := sha256.Sum256(data)
	if defErr != nil {
		defErr = fmt.Errorf("encode the definition of session %q: %w", info.Name, defErr)
	}
	return data, sum[:], defErr
}

// decodeInfo reads what encodeInfo wrote.
func decodeInfo(data []byte, codec DefinitionCodec) (*store.SessionInfo, error) {
	var si storedInfo
	if err := json.Unmarshal(data, &si); err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}
	info := &store.SessionInfo{
		Name: si.Name, Target: si.Target, Mode: si.Mode, Labels: si.Labels, Count: si.Count,
		Interval: si.Interval, Padding: si.Padding, DSCP: si.DSCP, ControlPort: si.ControlPort,
	}
	if len(si.Definition) > 0 && codec.Decode != nil {
		def, err := codec.Decode(si.Definition)
		if err != nil {
			return info, fmt.Errorf("decode the definition of session %q: %w", si.Name, err)
		}
		info.Definition = def
	}
	return info, nil
}

// encodePayload returns the payload of the Summary or the RunError of e,
// compressed by c (compact.go).
func encodePayload(c *compactPayload, e *store.Event) ([]byte, error) {
	switch e.Kind {
	case store.EventRunSummary, store.EventWindowSummary:
		if e.Summary == nil {
			return nil, fmt.Errorf("%v without a summary", e.Kind)
		}
		return c.encode(e.Summary)
	case store.EventRunError:
		if e.Error == nil {
			return nil, fmt.Errorf("%v without an error", e.Kind)
		}
		return c.encode(e.Error)
	default:
		return nil, fmt.Errorf("%v is not kept", e.Kind)
	}
}

// decodePayload fills the Summary or the RunError of e from data, in either
// payload format.
func decodePayload(e *store.Event, payload []byte) error {
	data, err := payloadJSON(payload)
	if err != nil {
		return err
	}
	switch e.Kind {
	case store.EventRunSummary, store.EventWindowSummary:
		e.Summary = new(sender.Summary)
		return json.Unmarshal(data, e.Summary)
	case store.EventRunError:
		e.Error = new(store.RunError)
		return json.Unmarshal(data, e.Error)
	default:
		return fmt.Errorf("unknown result kind %d", e.Kind)
	}
}

// kept reports whether the database keeps e: the summaries and run errors
// of named sessions, as Memory does.
func kept(e *store.Event) bool {
	if e.Session == nil || e.Session.Name == "" {
		return false
	}
	switch e.Kind {
	case store.EventRunSummary, store.EventWindowSummary, store.EventRunError:
		return true
	default:
		return false
	}
}
