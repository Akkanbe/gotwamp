package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/proto"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
)

// pingPrinter renders the events of a RunTest stream: as lines while they
// arrive (table), as one JSON line each (jsonl), or as one JSON document at
// the end (json).
type pingPrinter struct {
	w      io.Writer
	output string
	quiet  bool

	started *gotwampv1.RunStarted
	summary *gotwampv1.RunSummary
	runErr  *gotwampv1.RunError
	// replies and lost count what arrived, for an interrupted run.
	replies, lost int
	// doc collects the events of the json output.
	doc []json.RawMessage
}

// newPingPrinter returns the printer of one ping.
//
//declscope:package // runPing renders the stream through it
func newPingPrinter(w io.Writer, output string, quiet bool) *pingPrinter {
	return &pingPrinter{w: w, output: output, quiet: quiet}
}

// event renders one event.
//
//declscope:package // runPing renders the stream through it
func (p *pingPrinter) event(ev *gotwampv1.TestEvent) error {
	switch e := ev.GetEvent().(type) {
	case *gotwampv1.TestEvent_Started:
		p.started = e.Started
	case *gotwampv1.TestEvent_Packet:
		p.replies++
	case *gotwampv1.TestEvent_Lost:
		p.lost++
	case *gotwampv1.TestEvent_Summary:
		p.summary = e.Summary
	case *gotwampv1.TestEvent_Error:
		p.runErr = e.Error
	}
	perPacket := ev.GetPacket() != nil || ev.GetLost() != nil
	switch p.output {
	case "jsonl":
		if p.quiet && perPacket {
			return nil
		}
		return p.printJSON(ev)
	case "json":
		if p.quiet && perPacket {
			return nil
		}
		b, err := jsonl.Marshal(ev)
		if err != nil {
			return err
		}
		p.doc = append(p.doc, b)
		return nil
	}
	switch e := ev.GetEvent().(type) {
	case *gotwampv1.TestEvent_Started:
		return p.printf("%s\n", startLine(e.Started))
	case *gotwampv1.TestEvent_Packet:
		if !p.quiet {
			return p.printf("%s\n", packetLine(e.Packet))
		}
	case *gotwampv1.TestEvent_Lost:
		if !p.quiet {
			return p.printf("seq=%-3d *LOST*\n", e.Lost.GetSeq())
		}
	case *gotwampv1.TestEvent_Summary:
		return p.printf("%s", summaryText(p.target(), e.Summary))
	case *gotwampv1.TestEvent_Error:
		return p.printf("error in %s: %s\n", stageName(e.Error.GetStage()), e.Error.GetMessage())
	}
	return nil
}

// finish writes what is written at the end: the json document.
//
//declscope:package // runPing ends the output through it
func (p *pingPrinter) finish() error {
	if p.output != "json" {
		return nil
	}
	b, err := json.MarshalIndent(map[string][]json.RawMessage{"events": p.doc}, "", "  ")
	if err != nil {
		return err
	}
	return p.printf("%s\n", b)
}

// interrupted reports a run cut short by SIGINT, whose summary the daemon
// could no longer deliver.
//
//declscope:package // runPing reports interruptions through it
func (p *pingPrinter) interrupted() error {
	if err := p.finish(); err != nil {
		return err
	}
	if p.output != "table" {
		return nil
	}
	return p.printf("--- %s TWAMP statistics (interrupted) ---\n%d replies, %d lost so far\n", p.target(), p.replies, p.lost)
}

// localStage reports whether a run failed on this side (the test packets,
// gotwampd itself) rather than in the exchange with the server.
func localStage(s gotwampv1.RunStage) bool {
	return s == gotwampv1.RunStage_RUN_STAGE_TEST || s == gotwampv1.RunStage_RUN_STAGE_INTERNAL
}

// exitStatus is ExitOK when at least one reply arrived (SPEC §10.4).
//
//declscope:package // runPing exits with it
func (p *pingPrinter) exitStatus() error {
	if e := p.runErr; e != nil && p.summary == nil {
		code := ExitProtocol
		if localStage(e.GetStage()) {
			code = ExitFailure
		}
		if p.output == "table" {
			return exitf(code, "") // the error line is already out
		}
		return exitf(code, "%s: %s", stageName(e.GetStage()), e.GetMessage())
	}
	if p.summary == nil {
		return exitf(ExitFailure, "gotwampd ended the run without a summary")
	}
	// A control failure after the summary (control connection lost,
	// Stop-Sessions failed) leaves results marked invalid (SPEC §5.6): they
	// are shown, but the run failed (SPEC §10.4).
	if e := p.runErr; e != nil && !localStage(e.GetStage()) || p.summary.GetInvalid() {
		if p.output == "table" {
			return exitf(ExitProtocol, "")
		}
		msg := "the results are invalid"
		if e := p.runErr; e != nil {
			msg = stageName(e.GetStage()) + ": " + e.GetMessage()
		}
		return exitf(ExitProtocol, "%s", msg)
	}
	if p.summary.GetReceived() == 0 {
		// The summary already says it; exit quietly.
		return exitf(ExitFailure, "")
	}
	return nil
}

func (p *pingPrinter) printJSON(m proto.Message) error {
	b, err := jsonl.Marshal(m)
	if err != nil {
		return err
	}
	return p.printf("%s\n", b)
}

func (p *pingPrinter) printf(format string, args ...any) error {
	_, err := fmt.Fprintf(p.w, format, args...)
	return err
}

func (p *pingPrinter) target() string {
	if p.started != nil {
		if t := p.started.GetSpec().GetTarget(); t != "" {
			return t
		}
		return p.started.GetRemote().GetAddress()
	}
	return "target"
}

// startLine is the first line of a ping (SPEC §10.3): for Full the
// TWAMP-Control server, for Light the local socket, then the reflector.
func startLine(s *gotwampv1.RunStarted) string {
	spec := s.GetSpec()
	mode := strings.ToLower(strings.TrimPrefix(spec.GetMode().String(), "MODE_"))
	from := endpoint(s.GetLocal())
	if spec.GetMode() == gotwampv1.Mode_MODE_FULL {
		host := spec.GetTarget()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		from = fmt.Sprintf("%s:%d", host, spec.GetControlPort())
	}
	return fmt.Sprintf("TWAMP %s %s -> reflector %s, %d packets, interval %v, padding %d, dscp %s",
		mode, from, endpoint(s.GetRemote()), spec.GetCount(),
		spec.GetInterval().AsDuration(), spec.GetPadding(), dscpName(spec.GetDscp()))
}

// dscpName returns the name of a DSCP value (ef, afXY, csN, va), or its
// number.
func dscpName(v uint32) string {
	switch {
	case v == 46:
		return "ef"
	case v == 44:
		return "va"
	case v != 0 && v%8 == 0 && v <= 56:
		return fmt.Sprintf("cs%d", v/8)
	case v >= 10 && v <= 38 && v%2 == 0 && (v%8)/2 >= 1 && (v%8)/2 <= 3:
		return fmt.Sprintf("af%d%d", v/8, (v%8)/2)
	}
	return fmt.Sprint(v)
}
