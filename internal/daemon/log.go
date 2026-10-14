package daemon

import (
	"fmt"
	"io"
	"log/slog"
)

// NewLogger returns the daemon logger (SPEC §9.8): format is "json" or
// "text", level one of debug, info, warn and error. The level can be changed
// later through the returned LevelVar; pass it in Options.LogLevel so that a
// reload applies daemon.log.level. Durations read as "1.5ms" in both
// formats, rather than as nanoseconds in JSON.
func NewLogger(w io.Writer, format, level string) (*slog.Logger, *slog.LevelVar, error) {
	lv := new(slog.LevelVar)
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, nil, fmt.Errorf("log level %q: %w", level, err)
	}
	opts := &slog.HandlerOptions{Level: lv, ReplaceAttr: durationText}
	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), lv, nil
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), lv, nil
	default:
		return nil, nil, fmt.Errorf("log format %q is not json or text", format)
	}
}

// durationText writes a duration as time.Duration.String does.
func durationText(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindDuration {
		a.Value = slog.StringValue(a.Value.Duration().String())
	}
	return a
}
