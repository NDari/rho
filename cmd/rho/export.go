package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/session"
)

func newExportCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "export [session file]",
		Short: "Save a session as an HTML page",
		Long: "Save a session as a self-contained HTML page. Without a file, export the most recent session in the current directory.\n" +
			"The page is written to rho-<session>.html in the current directory unless -o says otherwise; -o - writes to stdout.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			} else {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				home, err := config.HomeDir()
				if err != nil {
					return err
				}
				if path, err = session.Latest(session.Dir(home, cwd)); err != nil {
					return err
				}
				if path == "" {
					return errors.New("no session in this directory; give a session file")
				}
			}
			sess, err := session.Open(path)
			if err != nil {
				return err
			}
			defer sess.Close()
			if out == "-" {
				return app.ExportHTML(cmd.OutOrStdout(), sess)
			}
			if out == "" {
				out = app.DefaultExportName(sess)
			}
			if err := app.WriteExport(out, sess); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "exported to", out)
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "file to write (- for stdout)")
	return cmd
}
