# Feature requests

Ideas for Rho, mostly the TUI and GUI, that are not built yet, with the notes from discussing them. Nothing here is decided until it moves into the spec ([rho-spec.md](rho-spec.md)).

| # | Feature | TUI | GUI | Status |
|---|---|---|---|---|
| 1 | File restore and a changes view | yes | yes | to discuss |
| 2 | Edit the prompt in an external editor (Ctrl+G) | done | no | done |
| 3 | Notifications when a turn needs you | yes | yes | on hold |
| 4 | Sessions sidebar | no | yes | GUI only |
| 5 | Open another project (`/open`) | no | done | GUI only |
| 6 | Terminal in the GUI | no | yes | needs dependencies |
| 7 | Built-in source code for the model | core | core | to discuss |
| 8 | Shared theme set (UI, syntax highlighting, export) | done | done | done |

## 1. File restore and a changes view

**To discuss.** A diff view of what the agent changed is wanted, especially in the GUI; restoring files on rewind has many edge cases to settle first.

### The idea

Today `/rewind` only rewinds the conversation and shows a banner that files on disk are not reverted (spec 7.5). The proposal is a store of file contents kept by Rho itself, not git:

- `edit` and `write` already read a file before changing it. They would save that content (or "did not exist") and the new content as blobs named by their hash, in a folder next to the session file, for example `~/.rho/sessions/<session>.files/`.
- The tool result entry records the path and both hashes. That is a new field on a new entry, so session files stay append-only.
- `/rewind` puts back the "before" content of every file changed after the chosen point, newest first, and deletes files the agent created.
- Since "after" is stored too, a `/tree` checkout could move the files to any branch: undo back to the common ancestor, then apply the other branch's changes.
- The same records give a **changes view**: the files changed in the last turn or the session, with their diffs, and a way to revert one file. In the GUI this could become a review pane where comments on lines are sent back as a follow-up message.

It works for any file content, but only `edit` and `write` write files today, so in practice it covers text.

### Not covered

- Changes made through `bash` (`sed -i`, `npm install`, `go generate`, `rm`, `git checkout`, ...).
- Changes made by the user or other programs in the meantime.
- Effects outside the project: databases, network calls, installed packages.

### The git alternative

Some harnesses snapshot the whole project into a hidden git repository before each turn. That also covers `bash`, but it needs a `git` binary (Rho has no external dependencies today), scans the whole tree on every snapshot (slow in large repositories), must decide what to do with ignored folders like `node_modules`, and snapshots files the agent never touched.

### Edge cases to settle

- **Conflicts:** the file changed after the agent wrote it (by the user, an editor, `bash`). Compare with the recorded "after" hash and skip and list such files instead of overwriting them? Offer to overwrite anyway?
- **Partial restores:** some files restore and others conflict. Is the rewind still done, and how is that shown?
- **Files outside the project folder:** the tools accept any path. Restore them, ask, or refuse?
- **Moves and deletes:** a `bash` `mv` or `rm` of a file the agent created or edited. The file is gone or somewhere else; what does restore do?
- **File modes, symbolic links and line endings:** restore the mode bits; write through or replace a link; keep CRLF files as they were.
- **Large files:** a size limit above which the content is not kept and the file is marked as not restorable.
- **Secrets:** blobs copy files such as `.env` into `~/.rho`. Same permissions as the session files? Excluded patterns?
- **Storage and cleanup:** when are blobs deleted (with the session, after some time, `/compact`)? Forks share blobs with the session they came from.
- **Two Rho instances in one project:** each only knows its own changes.
- **Windows:** files locked by another program.
- **Undo of a restore:** keep the pre-restore content too, so a mistaken rewind can be taken back?
- **Scope of the changes view:** last turn, whole session, current branch only, or the working tree compared with git `HEAD` when there is a repository.

## 2. Edit the prompt in an external editor (TUI)

**Done.** Ctrl+G opens the current input in `$VISUAL` or `$EDITOR` (falling back to `vi`, or `notepad` on Windows), as in other terminal agents. Rho writes the input to a temporary `.md` file, suspends the TUI while the editor runs (Bubble Tea's `tea.ExecProcess`) and puts the saved text back into the input. Not sent automatically. An empty file or a non-zero exit leaves the input unchanged.

Not needed in the GUI.

## 3. Notifications (both)

**On hold** until the macOS check below is done. Tell the user when Rho needs them while they are looking elsewhere. No core change is needed: the frontends already get `turn_end` and the UI requests.

### Design (agreed)

- **When:** a turn ends (done, failed after retries, or cancelled by an extension or RPC client, not by the user, who is there), or an extension asks a question (`ui/confirm`, `ui/select`, `ui/input`).
- **Only when the window is not focused.** Where focus is unknown (the terminal never reports it), notify only for turns that took more than about 10 seconds.
- **Content:** "Rho", the project folder name and what happened ("turn finished", "needs your answer"). Never the model's text, since notifications can show on a locked screen. In the GUI, clicking the notification brings the window forward.
- **On by default.**
- **TUI:** `tui.notify = auto | osc9 | osc777 | bell | off`. `auto` picks OSC 9 or OSC 777 from the terminal's environment variables (`TERM_PROGRAM` and similar) and falls back to the bell. Send one sequence, never both: terminals that support both would notify twice.
- **GUI:** a system notification, and on macOS perhaps a dock badge (not checked).

### What was checked (2026-11-24)

**TUI.** Bubble Tea v2 reports focus (`View.ReportFocus`, then `FocusMsg` and `BlurMsg`; in tmux this needs `focus-events on`). Escape sequences go out with `tea.Raw`; `x/ansi` has no notification helper, so the sequences are written by hand (OSC 777 is `ESC ] 777 ; notify ; title ; body ST` and has no escaping, so semicolons and control characters must be stripped). Since they are escape sequences, the notification shows where the terminal runs, so it works over SSH, unlike `notify-send` or D-Bus from Rho. Support, mostly from other projects' tables (only Ghostty's and kitty's from their own docs):

| Terminal | OSC 9 | OSC 777 |
|---|---|---|
| iTerm2 | yes | |
| kitty | yes (compatibility; its own is OSC 99) | |
| WezTerm | yes | yes |
| Ghostty | yes | yes (dropped while the surface is focused) |
| foot | | yes |
| VTE terminals (GNOME Terminal) | | only with Fedora's patch |
| Windows Terminal | no | 1.26 Preview and Canary only, off by default (`compatibility.allowOSC777`) |

No terminal answers a query for this, so `auto` must guess; stable Windows Terminal, Alacritty and stock GNOME Terminal get the bell. Inside tmux, OSC sequences need `allow-passthrough`; the bell passes through.

**GUI.** Wails v3.1.0 has a notifications service (`pkg/services/notifications`) with a pure-Go macOS file (`notifications_darwin_purego.go`), D-Bus on Linux through godbus (pure Go) and toasts on Windows, so it should build with `CGO_ENABLED=0`. On macOS it refuses to start without a bundle identifier: `Rho.app` has one (`gui/build/darwin/Info.plist`), a bare `rho-gui` (dev builds) does not, so that case must fail quietly.

### Still to check

- **macOS with an ad-hoc signed app.** Releases are ad-hoc signed unless the signing secrets are set (see [releasing.md](releasing.md)). No authoritative answer was found on whether macOS grants notification permission to an ad-hoc signed app downloaded from the internet; reports say only code-signed apps are allowed, and that ad-hoc signing may count. Test: build `Rho.app` ad-hoc signed, start it on a Mac, and see whether the permission prompt appears and a notification arrives.
- The Windows GUI toast and the Linux D-Bus notification, in real use.
- Terminals in the table not seen first hand.

References: [Ghostty VT reference](https://ghostty.org/docs/vt/reference), [kitty desktop notifications](https://sw.kovidgoyal.net/kitty/desktop-notifications/), [Windows Terminal OSC 777 issue](https://github.com/microsoft/terminal/issues/7718).

## 4. Sessions sidebar (GUI only)

**Ready.** `/resume` already picks a session, but only from the current folder. The GUI gets a sidebar, which can be hidden, that lists sessions from every project:

- grouped by project folder, newest first, with the title (first message or label), date, model and size,
- search by title and folder,
- rename (sets the label), delete (with a confirmation),
- picking a session in another folder switches to that project first, as `/open` does (see 5).

**Decided: no `/sessions` in the TUI.** It would overlap with `/resume` and only add the list across all projects. Picking a session from another folder would then need a project switch, which the TUI is not getting (see 5). In the terminal, `/resume` covers the current folder, and `rho --session FILE` or starting `rho -r` in the other folder covers the rest.

## 5. Open another project (GUI only)

**Decided: GUI only, and the GUI already has it.** The GUI has `/open` (with a folder dialog, or `/open path`). It is more useful there, since the GUI is often started from a launcher instead of in the project folder. In the terminal, starting `rho` in another folder does the same, so the TUI does not get `/open`, and the `/cd` idea is dropped.

Possible GUI additions: a list of recent project folders (the folders that have sessions) when `/open` has no path, next to the folder dialog.

## 6. Terminal in the GUI

**Needs dependencies.** A terminal panel at the bottom of the window, opened in the project folder, toggled with a key (Ctrl+` in many editors).

- Needs a pseudo terminal on the Go side (for example `creack/pty` on Linux and macOS, ConPTY on Windows) and a terminal widget in the frontend (for example xterm.js). Both are new dependencies to approve.
- A smaller first step: an **Open in terminal** action that starts the system terminal in the project folder.
- Later, the agent's `bash` output could be opened in the panel.

## 7. Built-in source code for the model

**To discuss.** The built-in docs (`rho docs`, installed in `~/.rho/docs/<hash>/`) let a model learn the extension API. Embedding the source code as well would let it check the details the docs leave out: the order hooks run in, timeouts, which fields are set when, and what happens on failure.

### The idea

- Embed the non-test `.go` files of `core/`, `cmd/`, `tui/` and `headless/`, plus `go.mod`, next to the docs, in a `source/` directory of the installed copy. Leave out tests, `gui/`, `Taskfile.yml`, CI and the docs about developing Rho.
- Cost: about 490 KB (roughly 0.5% of the `rho` binary), no tokens unless the model reads a file, and half a megabyte more per installed copy.
- It always matches the binary and works offline, which matters on locked-down machines that cannot reach the repository.
- The `rho-extensions` skill would give the exact version (`rho --version`) and the repository URL, so a model that is online can clone the matching tag to change Rho itself. The embedded source is a reference, not a build kit.

### Why not yet

- **Internals are not the API.** A model reading the source may build an extension on behavior that is not promised and changes between versions. If this is done, the skill must say that `extensions.md` defines the API and the source is only for checking details, and it may be worth embedding only the API's own definitions first (`core/ext/protocol.go` and `core/ext/yaegihost/api.go`, about 13 KB).
- Better docs may cover the same need: answering in `extensions.md` the questions a model would otherwise answer from the code.

## 8. Shared theme set (both, and the HTML export)

**Done.** Rho's own two themes were replaced by 17 public ones (Gruvbox, GitHub, Catppuccin, Tokyo Night, Rosé Pine, Solarized and Kanagawa in dark and light, plus Dracula, Nord and One Dark), each taken from its project's published palette and paired with its Chroma style. They live in `core/theme` as embedded JSONC files in the format of `~/.rho/themes/`, where a user theme starts from any `base`; roles a theme leaves unset are derived from its main colors. One `theme` setting (or `system`, with `theme_dark` and `theme_light`) drives the TUI, the GUI (CSS variables, and highlight.js colors from the Chroma style) and the HTML export (Chroma's HTML output for code). `/theme` previews each theme as the cursor moves in both frontends; the TUI paints the terminal's background, as Vim does. Unknown theme, base or Chroma names give a warning. See [configuration.md](configuration.md#themes).

### Not taken

- **bubbletint** (`github.com/lrstanley/bubbletint/v2`): over 770 schemes from iTerm2-Color-Schemes, with Chroma and Glamour mappings and following the terminal's theme. Its schemes are terminal palettes (the 16 ANSI colors plus foreground and background), so every Rho role would still be derived automatically, and 770 unchecked themes would vary in quality. It could come back later for "match my terminal".

## Other ideas from the same review

Mentioned while comparing Rho with other harnesses and editors, not requested yet:

- **Images:** paste or drop a screenshot into the input for models with `supports_images`. In the TUI, paste an image path or read an image from the clipboard.
- **Edit an earlier prompt and send it again** in one step, instead of `/rewind` and retyping.
- **Custom key bindings in the TUI:** `tui.keybindings` is read but not applied yet.
- **GUI command palette** (Ctrl+K).
