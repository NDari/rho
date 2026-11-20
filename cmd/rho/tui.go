package main

import (
	"errors"
	"os"

	"golang.org/x/term"

	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/update"
	"github.com/NDari/rho/tui"
)

func runTUI(f rootFlags, text string) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("the interactive TUI needs a terminal; use -p for non-interactive use")
	}
	a, err := app.New(app.Options{
		Model:        f.model,
		Continue:     f.cont,
		SessionFile:  f.sessionF,
		Version:      version,
		NoExtensions: f.noExt,
	})
	if err != nil {
		return err
	}
	o := tui.Options{Prompt: text, Resume: f.resume}
	if update.Enabled(current, a.Config) {
		o.Updates = &update.Checker{Current: current, Home: a.Paths.Home}
	}
	return tui.Run(a, o)
}
