package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/docs"
)

func newDocsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "docs [topic or file]",
		Short: "Print Rho's built-in documentation and examples",
		Long: "Print Rho's built-in documentation and examples. With no argument, list them.\n" +
			"The same files are installed under the Rho home (shown in the list), so a model can read and search them.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				data, err := docs.Read(args[0])
				if err != nil {
					return err
				}
				_, err = out.Write(data)
				return err
			}
			fmt.Fprintln(out, "Topics (rho docs <topic>):")
			for _, t := range docs.Topics {
				fmt.Fprintf(out, "  %-14s %s\n", t.Name, t.Summary)
			}
			fmt.Fprintln(out, "\nExamples (rho docs <file>):")
			for _, e := range docs.Examples() {
				fmt.Fprintf(out, "  %s\n", e)
			}
			if home, err := config.HomeDir(); err == nil {
				if dir, err := docs.Install(home); err == nil {
					fmt.Fprintf(out, "\nInstalled copy: %s\n", dir)
				}
			}
			return nil
		},
	}
}
