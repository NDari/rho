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

Configure models in `~/.rho/config.toml` first, as described in the [README](../README.md#quick-start) and [configuration.md](configuration.md). If Rho cannot start (for example because no model is configured yet), the window shows the error with an example configuration for a local llama.cpp server. When there is no config file yet, **Create the config file with this example** writes it and opens the project; otherwise fix the file and press **Try again**.

To work in another project, use `/open` (or **Open another folder…** in an empty conversation) to pick a folder. `/open path/to/project` skips the dialog. Switching starts a new session in that folder.

## Requirements

The GUI draws its window with the system webview, which must be installed:

| Platform | Needs |
|---|---|
| Windows 10/11 | WebView2 (included with current Windows versions) |
| macOS | nothing extra |
| Linux | GTK 4.8+ and WebKitGTK 6.0 (2.40+), e.g. `libgtk-4-1` and `libwebkitgtk-6.0-4` on Debian and Ubuntu, `gtk4` and `webkitgtk-6.0` on Arch |

The `rho` command needs none of these; it stays a single static binary.

The GUI downloads contain both `rho-gui` and `rho`, and `Rho.app` has both inside. Keep the two side by side: the GUI uses `rho` to run Go extensions, which starts them faster and with less memory than running them through `rho-gui`.

## Installing

Each [release](https://github.com/NDari/rho/releases/latest) has the GUI downloads next to the `rho`-only ones:

| Platform | File |
|---|---|
| macOS (Apple silicon) | `Rho_<version>_macos.zip` |
| Linux (x86-64 or ARM64) | `rho-gui_<version>_linux_<arch>.tar.gz` |
| Windows (x86-64) | `rho-gui_<version>_windows_amd64.zip` |

- **macOS:** unzip the file and move `Rho.app` to Applications. Unless the release is signed and notarized, macOS blocks the first start; allow it under System Settings, Privacy & Security, or remove the quarantine flag with `xattr -dr com.apple.quarantine /Applications/Rho.app`. For the terminal, install `rho_<version>_darwin_arm64.tar.gz` as well; the `rho` inside `Rho.app` is for the GUI and is updated with it.
- **Linux:** unpack the archive and put `rho-gui` and `rho` together in a folder on your `PATH`, for example `~/.local/bin`. That `rho` is the same as the one in `rho_<version>_linux_<arch>.tar.gz`, so it also gives you the TUI. For an entry in the application menu with the Rho icon, install the files from the archive's `linux/` folder:

  ```sh
  mkdir -p ~/.local/share/applications ~/.local/share/icons/hicolor/scalable/apps
  cp linux/io.github.ndari.rho.desktop ~/.local/share/applications/
  cp linux/io.github.ndari.rho.svg ~/.local/share/icons/hicolor/scalable/apps/
  ```

  Wayland desktops take the window icon from this entry, so without it the window shows a generic icon.
- **Windows:** unzip the file and keep `rho-gui.exe` and `rho.exe` together.

## Updating

Release builds look for a new version once a day. When there is one, a notice in the top right corner offers **Update and restart**: Rho downloads the release, checks it against the release's checksums, replaces `rho-gui` and `rho` (or `Rho.app`) and opens again in the same folder and session. The button waits while the agent is working. **Later** hides the notice until the next start.

The GUI keeps the result of its last check in `~/.rho/update-gui.json`. When the folder the GUI runs from is not writable, the update goes to `~/.local/bin` (`~/Applications` for `Rho.app`, `%LOCALAPPDATA%\Programs\Rho` on Windows) and the notice says what to do about the old copy and your `PATH`. On Linux, a desktop entry that starts `rho-gui` from the old folder keeps starting the old copy until you change it. See [configuration.md](configuration.md#updates) to turn the check off.

## Using it

The window works like the TUI:

- **Enter** sends, **Shift+Enter** or **Alt+Enter** adds a line.
- While the agent works, **Enter** steers it (interrupts after the current tool), **Alt+Enter** queues a follow-up and **Esc** (or the Stop button) cancels.
- Type `/` for commands: `/model`, `/new`, `/open`, `/resume`, `/rewind`, `/fork`, `/tree`, `/aside`, `/back`, `/compact`, `/context`, `/label`, `/copy`, `/export`, `/theme`, `/config`, `/extensions`, `/reload`, `/help` and `/quit`, plus prompt templates and extension commands. **Tab** completes a command.
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

With [Task](https://taskfile.dev), `task build` does this (and builds `rho`) into `bin/`, and `task gui` also starts the GUI; see the [README](../README.md#working-on-rho) for the other tasks.

The GUI is built with [Wails](https://wails.io) v3 and its pure-Go backend (the `purego` tag), so no C compiler is needed and you can cross-compile with `GOOS` and `GOARCH`. On Windows, add `-ldflags "-H windowsgui"` so no console window opens.

The icons are generated from `gui/build/appicon.svg` with `sh gui/build/icons.sh`. Release builds also run `gui/build/windows/syso.sh`, which adds the icon, a high-DPI manifest and version information to the Windows program, and `gui/build/darwin/bundle.sh` (on macOS), which builds `Rho.app`. A build from source has no version, so it does not look for updates.

The GUI lives in its own Go module (`gui/`), so building or testing the rest of Rho never needs Wails or the webview libraries. See section 18 of the [design spec](rho-spec.md) for how it is put together.

## Signing macOS releases

See [releasing.md](releasing.md#signing-macos-releases) for the secrets that turn on signing and notarization.
