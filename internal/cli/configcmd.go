package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	gotwampv1 "github.com/Akkanbe/gotwamp/internal/api/gen/gotwamp/v1"
	"github.com/Akkanbe/gotwamp/internal/config"
)

// newConfigCommand returns "gotwamp config" and its subcommands.
//
//declscope:package // a subcommand of the root
func newConfigCommand(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Validate the gotwampd configuration",
	}
	var file string
	validate := &cobra.Command{
		Use:   "validate -f FILE",
		Short: "Check a configuration file without gotwampd",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return validateConfig(g, cmd, file)
		},
	}
	validate.Flags().StringVarP(&file, "file", "f", config.DefaultPath, "configuration file")
	reload := &cobra.Command{
		Use:   "reload",
		Short: "Make gotwampd re-read its configuration file and apply the changes (like SIGHUP)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := g.conn()
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), g.timeout)
			defer cancel()
			resp, err := gotwampv1.NewDaemonServiceClient(conn).ReloadConfig(ctx, &gotwampv1.ReloadConfigRequest{})
			if err != nil {
				return g.rpcError(err)
			}
			if g.output != "table" {
				return g.printProto(resp)
			}
			w := cmd.OutOrStdout()
			list := func(l []string) string {
				if len(l) == 0 {
					return "-"
				}
				return strings.Join(l, ", ")
			}
			_, err = fmt.Fprintf(w, "sessions added:   %s\nsessions removed: %s\nsessions updated: %s\nsessions unchanged: %d\n",
				list(resp.GetSessionsAdded()), list(resp.GetSessionsRemoved()), list(resp.GetSessionsUpdated()), resp.GetSessionsUnchanged())
			for _, warn := range resp.GetWarnings() {
				_, _ = fmt.Fprintln(w, "warning:", warn)
			}
			return err
		},
	}
	cmd.Short = "Validate or reload the gotwampd configuration"
	cmd.AddCommand(validate, reload)
	return cmd
}

// validation is the JSON output of config validate.
type validation struct {
	File     string   `json:"file"`
	Valid    bool     `json:"valid"`
	Problems []string `json:"problems,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func validateConfig(g *globals, cmd *cobra.Command, file string) error {
	c, err := config.Load(file)
	res := validation{File: file, Valid: err == nil}
	var verr *config.Error
	switch {
	case errors.As(err, &verr):
		for _, p := range verr.Problems {
			res.Problems = append(res.Problems, p.String())
		}
	case err != nil:
		res.Problems = []string{err.Error()}
	default:
		for _, w := range c.Warnings() {
			res.Warnings = append(res.Warnings, w.String())
		}
	}

	out := cmd.OutOrStdout()
	if g.output != "table" {
		enc := json.NewEncoder(out)
		if g.output == "json" {
			enc.SetIndent("", "  ")
		}
		if err := enc.Encode(res); err != nil {
			return err
		}
	} else {
		for _, w := range res.Warnings {
			_, _ = fmt.Fprintln(out, "warning:", w)
		}
		for _, p := range res.Problems {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "error:", p)
		}
		if res.Valid {
			_, _ = fmt.Fprintf(out, "%s: configuration is valid\n", file)
		}
	}
	if !res.Valid {
		return exitf(ExitUsage, "%s: configuration is invalid (%d problems)", file, len(res.Problems))
	}
	return nil
}
