package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/update"
)

// current is the running program, set by main from version.
var current = update.Current{Version: version}

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update Rho to the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdate(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

func runUpdate(ctx context.Context, out io.Writer) error {
	switch current.Method {
	case update.Dev:
		return errors.New("this is a development build without a version; build it again from source, or install a release")
	case update.GoInstall:
		fmt.Fprintf(out, "Rho %s was installed with go install. Update it with:\n\n  %s\n", current.Version, update.GoInstallCmd)
		return nil
	}
	home, err := config.HomeDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := &update.Checker{Current: current, Home: home}
	fmt.Fprintln(out, "Looking for updates...")
	rel, err := c.Latest(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(rel.Version, current.Version) {
		fmt.Fprintf(out, "Rho %s is up to date.\n", current.Version)
		return nil
	}
	fmt.Fprintf(out, "Updating Rho %s to %s...\n", current.Version, rel.Version)
	res, err := c.Install(ctx, rel, exe)
	if err != nil {
		return err
	}
	where := filepath.Dir(res.Exe)
	if res.App != "" {
		where = res.App
	}
	fmt.Fprintf(out, "Rho %s is installed in %s.\n", res.Version, where)
	for _, n := range res.Notes {
		fmt.Fprintln(out, n)
	}
	return nil
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the global config file with an example configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := config.HomeDir()
			if err != nil {
				return err
			}
			path := config.Paths{Home: home}.GlobalFile()
			if err := config.CreateExample(path); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s:\n\n%s\n%s\n", path, indent(config.Example), exampleNote)
			return nil
		},
	}
}

const exampleNote = "It uses a local llama.cpp server at http://localhost:9931/v1 (older llama.cpp versions use port 8080).\n" +
	"Edit it to match your server, or see the configuration guide for hosted providers."

// noModelHint explains how to configure a model after config.ErrNoModel.
func noModelHint() string {
	home, err := config.HomeDir()
	if err != nil {
		return ""
	}
	path := config.Paths{Home: home}.GlobalFile()
	if _, err := os.Stat(path); err != nil {
		return fmt.Sprintf("\nTo get started, run 'rho init' to create %s with this example, then edit it:\n\n%s\n%s\n", path, indent(config.Example), exampleNote)
	}
	return fmt.Sprintf("\nAdd a model to %s, for example:\n\n%s\n%s\n", path, indent(config.Example), exampleNote)
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = "  " + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
