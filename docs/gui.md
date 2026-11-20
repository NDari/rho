# Rho GUI

`rho-gui` is a desktop frontend for Rho. It uses the same configuration, sessions, skills, prompt templates and extensions as the `rho` command, so you can move between the two: a session started in the terminal can be resumed in the GUI and the other way around.

> **Status:** early. The GUI covers the main workflow (chat, tool cards, diffs, sessions and branching, settings, extensions) but has rough edges.

## Running

```sh
rho-gui                 # the current directory (or your home directory when started from a launcher)
rho-gui path/to/project
rho-gui -c              # continue the last session in the directory
rho-gui -m openai/<model-name>  # pick a model
rho-gui -session FILE   # open a specific session file
rho-gui -no-extensions
```

Configure models in `~/.rho/config.toml` first, as described in the [README](../README.md#quick-start) and [configuration.md](configuration.md). If Rho cannot start (for example because no model is configured yet), the window shows the error with an example configuration; fix the file and press **Try again**.

To work in another project, use `/open` (or **Open another folder…** in an empty conversation) to pick a folder. `/open path/to/project` skips the dialog. Switching starts a new session in that folder.

## Requirements

The GUI draws its window with the system webview, which must be installed:

| Platform | Needs |
|---|---|
| Windows 10/11 | WebView2 (included with current Windows versions) |
| macOS | nothing extra |
| Linux | GTK 4.8+ and WebKitGTK 6.0 (2.40+), e.g. `libgtk-4-1` and `libwebkitgtk-6.0-4` on Debian and Ubuntu, `gtk4` and `webkitgtk-6.0` on Arch |

The `rho` command needs none of these; it stays a single static binary.

The `rho-gui` archives also contain `rho`. Keep the two side by side: the GUI uses `rho` to run Go extensions, which starts them faster and with less memory than running them through `rho-gui`.

## Using it

The window works like the TUI:

- **Enter** sends, **Shift+Enter** or **Alt+Enter** adds a line.
- While the agent works, **Enter** steers it (interrupts after the current tool), **Alt+Enter** queues a follow-up and **Esc** (or the Stop button) cancels.
- Type `/` for commands: `/model`, `/new`, `/open`, `/resume`, `/rewind`, `/fork`, `/tree`, `/aside`, `/back`, `/compact`, `/context`, `/label`, `/copy`, `/theme`, `/config`, `/extensions`, `/reload`, `/help` and `/quit`, plus prompt templates and extension commands. **Tab** completes a command.
- `@path` attaches a file, as in the TUI; typing `@` completes file names. **Esc** hides a completion.
- **Ctrl+F** finds text in the conversation (**Enter** and **Shift+Enter** move between matches). Collapsed tool output is not searched; **Ctrl+O** expands it.
- Click a tool card's header to expand or collapse it. **Ctrl+O** expands all of them, **Ctrl+T** shows the model's thinking.
- `/theme` picks light, dark or the system setting. The GUI keeps this choice itself; the `theme` setting in the config file is for the TUI.
- Links in answers open in your browser.

## Building from source

You need Go 1.27 or newer and Node.js 22.12 or newer. From the repository root:

```sh
cd gui/frontend
npm ci
npm run build    # generates the Go bindings and builds the web assets into dist/
cd ..
CGO_ENABLED=0 go build -tags purego,production -o rho-gui .
```

The GUI is built with [Wails](https://wails.io) v3 and its pure-Go backend (the `purego` tag), so no C compiler is needed and you can cross-compile with `GOOS` and `GOARCH`. On Windows, add `-ldflags "-H windowsgui"` so no console window opens.

The GUI lives in its own Go module (`gui/`), so building or testing the rest of Rho never needs Wails or the webview libraries. See section 18 of the [design spec](rho-spec.md) for how it is put together.
