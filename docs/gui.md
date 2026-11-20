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

The Linux and Windows release archives contain both `rho-gui` and `rho`, and `Rho.app` has both inside. Keep the two side by side: the GUI uses `rho` to run Go extensions, which starts them faster and with less memory than running them through `rho-gui`.

## Installing

- **macOS:** download `Rho_<version>_macos.zip` from the release, unzip it and move `Rho.app` to Applications. It runs on Apple silicon Macs and contains both `rho-gui` and `rho`. Unless the release is signed and notarized, macOS blocks the first start; allow it under System Settings, Privacy & Security, or remove the quarantine flag with `xattr -dr com.apple.quarantine /Applications/Rho.app`. To use the TUI too, link the `rho` inside the app into a folder on your `PATH`, for example `ln -s /Applications/Rho.app/Contents/MacOS/rho ~/.local/bin/rho`; `rho update` then updates the whole app. If you only want the terminal, download `rho_<version>_darwin_arm64.tar.gz` instead.
- **Linux:** unpack `rho_<version>_linux_<arch>.tar.gz` and put `rho-gui` and `rho` on your `PATH`. For an entry in the application menu with the Rho icon, install the files from the archive's `linux/` folder:

  ```sh
  mkdir -p ~/.local/share/applications ~/.local/share/icons/hicolor/scalable/apps
  cp linux/io.github.ndari.rho.desktop ~/.local/share/applications/
  cp linux/io.github.ndari.rho.svg ~/.local/share/icons/hicolor/scalable/apps/
  ```

  Wayland desktops take the window icon from this entry, so without it the window shows a generic icon.
- **Windows:** unzip `rho_<version>_windows_amd64.zip` and keep `rho-gui.exe` and `rho.exe` together.

## Updating

Release builds look for a new version once a day. When there is one, a notice in the top right corner offers **Update and restart**: Rho downloads the release, checks it against the release's checksums, replaces `rho-gui` and `rho` (or `Rho.app`) and opens again in the same folder and session. The button waits while the agent is working. **Later** hides the notice until the next start.

When the folder the GUI runs from is not writable, the update goes to `~/.local/bin` (`~/Applications` for `Rho.app`, `%LOCALAPPDATA%\Programs\Rho` on Windows) and the notice says what to do about the old copy and your `PATH`. On Linux, a desktop entry that starts `rho-gui` from the old folder keeps starting the old copy until you change it. See [configuration.md](configuration.md#updates) to turn the check off.

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

The icons are generated from `gui/build/appicon.svg` with `sh gui/build/icons.sh`. Release builds also run `gui/build/windows/syso.sh`, which adds the icon, a high-DPI manifest and version information to the Windows program, and `gui/build/darwin/bundle.sh` (on macOS), which builds `Rho.app`. A build from source has no version, so it does not look for updates.

The GUI lives in its own Go module (`gui/`), so building or testing the rest of Rho never needs Wails or the webview libraries. See section 18 of the [design spec](rho-spec.md) for how it is put together.

## Signing macOS releases

Releases are signed and notarized when these repository secrets are set; otherwise the macOS binaries and `Rho.app` are unsigned (ad-hoc signed).

| Secret | Holds |
|---|---|
| `MACOS_SIGN_P12` | a Developer ID Application certificate with its private key, exported as `.p12` and base64 encoded |
| `MACOS_SIGN_PASSWORD` | the `.p12` password |
| `MACOS_NOTARY_KEY` | an App Store Connect API key (`.p8`), base64 encoded |
| `MACOS_NOTARY_KEY_ID` | that key's ID |
| `MACOS_NOTARY_ISSUER_ID` | the issuer ID shown with the key |

GoReleaser signs and notarizes the `rho` and `rho-gui` binaries in the archives, and the `macos-app` job of the release workflow signs, notarizes and staples `Rho.app`.
