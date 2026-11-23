package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/ext/yaegihost"
	"github.com/NDari/rho/headless"
	"github.com/NDari/rho/rpcmode"
)

// stopSignals cancel the turn in print and RPC mode: Ctrl-C; SIGTERM,
// which a service manager sends and which Windows sends when the console
// window is closed; and SIGHUP, sent when a Unix terminal is closed. The
// TUI gets SIGINT and SIGTERM from Bubble Tea.
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

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
	tools    []string
	noProj   bool
	system   string
	appendS  []string
	maxSteps int
	mode     string
}

func newRootCmd() *cobra.Command {
	var f rootFlags
	var toolList string
	cmd := &cobra.Command{
		Use:           "rho [prompt]",
		Short:         "A minimal-core coding agent",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("tools") {
				f.tools = app.ParseToolList(toolList)
			}
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
	fl.BoolVar(&f.noProj, "no-project-context", false, "ignore the project's .rho directory and AGENTS.md files (for untrusted checkouts)")
	fl.StringVar(&f.system, "system-file", "", "replace the system prompt with this file")
	fl.StringArrayVar(&f.appendS, "append-system-file", nil, "append this file to the system prompt (repeatable)")
	fl.IntVar(&f.maxSteps, "max-steps", 0, "with -p or --mode rpc, stop a turn with an error after this many model responses (0: no limit)")
	fl.StringVar(&f.mode, "mode", "", `"rpc": let another program drive Rho with JSON-RPC on stdin and stdout (see docs/rpc.md)`)
	fl.StringVar(&toolList, "tools", "", "comma-separated built-in tools to offer, replacing tools.enabled (e.g. read,grep,find,ls)")
	cmd.AddCommand(&cobra.Command{
		Use:    "ext-host <path>",
		Short:  "Run a Go source extension (used internally)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return yaegihost.Main(args[0])
		},
	})
	cmd.AddCommand(newUpdateCmd(), newInitCmd(), newDocsCmd(), newExportCmd())
	return cmd
}

func run(cmd *cobra.Command, f rootFlags, args []string) error {
	text := strings.Join(args, " ")
	switch f.mode {
	case "":
	case "rpc":
		if f.print || f.json || f.resume || text != "" {
			return errors.New("--mode rpc takes no prompt and cannot be combined with -p, --json or -r")
		}
		return runRPC(cmd, f)
	default:
		return fmt.Errorf("unknown --mode %q (the only mode is rpc)", f.mode)
	}
	if f.json && !f.print {
		return errors.New("--json requires -p")
	}
	if f.maxSteps != 0 && !f.print {
		return errors.New("--max-steps requires -p or --mode rpc")
	}
	if f.maxSteps < 0 {
		return errors.New("--max-steps must not be negative")
	}
	if f.print {
		return runPrint(cmd, f, text)
	}
	return runTUI(f, text)
}

func runRPC(cmd *cobra.Command, f rootFlags) error {
	ctx, stop := signal.NotifyContext(context.Background(), stopSignals...)
	defer stop()
	return rpcmode.Run(ctx, app.Options{
		Model:             f.model,
		Continue:          f.cont,
		SessionFile:       f.sessionF,
		Version:           version,
		NoExtensions:      f.noExt,
		Tools:             f.tools,
		NoProjectContext:  f.noProj,
		SystemFile:        f.system,
		AppendSystemFiles: f.appendS,
		MaxSteps:          f.maxSteps,
	}, cmd.InOrStdin(), cmd.OutOrStdout())
}

func runPrint(cmd *cobra.Command, f rootFlags, text string) error {
	if f.resume {
		return errors.New("-r needs the interactive TUI; use --session <file> or -c with -p")
	}
	piped, err := readStdin(os.Stdin, strings.TrimSpace(text) != "", cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(piped); s != "" {
		if text != "" {
			text += "\n\n"
		}
		text += s
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("no prompt given")
	}
	hopts := headless.Options{JSON: f.json, Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr()}
	a, err := app.New(app.Options{
		Model:        f.model,
		Continue:     f.cont,
		SessionFile:  f.sessionF,
		Version:      version,
		Sink:         headless.Sink(hopts),
		NoExtensions: f.noExt,
		Tools:        f.tools,

		NoProjectContext:  f.noProj,
		SystemFile:        f.system,
		AppendSystemFiles: f.appendS,
		MaxSteps:          f.maxSteps,
	})
	if err != nil {
		return err
	}
	for _, w := range a.Warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
	}
	ctx, stop := signal.NotifyContext(context.Background(), stopSignals...)
	defer stop()
	defer a.Agent.Session().Close()
	a.StartExtensions(ctx, headless.UI{ErrOut: cmd.ErrOrStderr()})
	defer a.StopExtensions()
	if err := headless.Run(ctx, a, text, hopts); err != nil {
		if errors.Is(err, context.Canceled) {
			return errors.New("cancelled")
		}
		return err
	}
	return nil
}
