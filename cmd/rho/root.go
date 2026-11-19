package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/NDari/rho/core/config"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type rootFlags struct {
	print    bool
	json     bool
	cont     bool
	resume   bool
	model    string
	noExt    bool
	sessionF string
}

func newRootCmd() *cobra.Command {
	var f rootFlags
	cmd := &cobra.Command{
		Use:           "rho [prompt]",
		Short:         "A minimal-core coding agent",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, f, args)
		},
	}
	fl := cmd.Flags()
	fl.BoolVarP(&f.print, "print", "p", false, "run one prompt non-interactively and print the result")
	fl.BoolVar(&f.json, "json", false, "with -p, stream events as JSON lines")
	fl.BoolVarP(&f.cont, "continue", "c", false, "continue the most recent session in this directory")
	fl.BoolVarP(&f.resume, "resume", "r", false, "pick a session to resume")
	fl.StringVarP(&f.model, "model", "m", "", "model id, or provider/name")
	fl.BoolVar(&f.noExt, "no-extensions", false, "do not load extensions")
	fl.StringVar(&f.sessionF, "session", "", "open a specific session file")
	return cmd
}

func run(cmd *cobra.Command, f rootFlags, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	paths, err := config.ResolvePaths(cwd)
	if err != nil {
		return err
	}
	cfg, warnings, err := config.Load(paths)
	for _, w := range warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
	}
	if err != nil {
		return err
	}
	model, err := cfg.ResolveModel(f.model)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "model: %s (%s)\n", model.ID, model.Provider)
	return nil
}
