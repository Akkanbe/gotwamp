package cli

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// newVersionCommand returns "gotwamp version". It does not contact gotwampd.
//
//declscope:package // a subcommand of the root
func newVersionCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version, commit and Go version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if g.output == "table" {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "gotwamp", g.info.String())
				return err
			}
			version, commit := g.info.Known()
			v := map[string]string{"version": version, "commit": commit, "go_version": runtime.Version()}
			enc := json.NewEncoder(cmd.OutOrStdout())
			if g.output == "json" {
				enc.SetIndent("", "  ")
			}
			return enc.Encode(v)
		},
	}
}
