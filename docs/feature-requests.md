# Feature requests

Ideas for Rho, mostly the TUI and GUI, that are not built yet, with the notes from discussing them. Nothing here is decided until it moves into the spec ([rho-spec.md](rho-spec.md)).

| # | Feature | TUI | GUI | Status |
|---|---|---|---|---|
| 1 | File restore and a changes view | yes | yes | to discuss |
| 2 | Edit the prompt in an external editor (Ctrl+G) | done | no | done |
| 3 | Notifications when a turn needs you | yes | yes | ready |
| 4 | Sessions sidebar | no | yes | GUI only |
| 5 | Open another project (`/open`) | no | done | GUI only |
| 6 | Terminal in the GUI | no | yes | needs dependencies |
| 7 | Built-in source code for the model | core | core | to discuss |
| 8 | Shared theme set (UI, syntax highlighting, export) | yes | yes | ready |

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

**Ready.** Tell the user when Rho needs them while they are looking elsewhere:

- when a turn ends (done, failed after retries, or cancelled),
- when an extension asks a question (`ui/confirm`, `ui/select`, `ui/input`).

Only when the window is not focused.

- **TUI:** a desktop notification through the terminal (OSC 9 or OSC 777, which many terminals support), or the bell. Focus comes from terminal focus reporting where the terminal supports it. A setting such as `tui.notify = auto | bell | off`.
- **GUI:** a system notification, and on macOS a dock badge. Check what Wails v3 offers for notifications and whether it works with the pure-Go backend on Linux; otherwise use D-Bus directly there.

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

**Ready.** A set of complete themes, each chosen with one `theme` setting, that changes everything at once: the UI colors, syntax highlighting, Markdown code blocks and diffs, in the TUI, the GUI and the HTML export. Users can add their own in the same format.

### Today

- **TUI:** a theme (`dark` or `light`, or a TOML file in `~/.rho/themes/`) sets about 22 color roles (accent, text, muted, border, user background, status bar, diff backgrounds and signs, ...) plus a `chroma` field that names one of Chroma's 74 syntax styles. The `chroma` style only colors code tokens; everything else comes from the roles. The built-ins pair Gruvbox with the `gruvbox` style and a GitHub-like light theme with `catppuccin-latte`. The `chroma` field is not documented, and an unknown style name silently falls back to Chroma's default.
- **GUI:** light, dark or system only, kept by the GUI itself; code is highlighted with highlight.js.
- **HTML export:** follows the system's light or dark scheme; code is not highlighted.

### The idea

- **Built-in themes:** the most popular themes with public, well documented palettes, about ten, each tuned by hand and paired with the matching Chroma style. A first list: Gruvbox Dark and Light, Catppuccin Mocha and Latte, Tokyo Night, Nord, Dracula, One Dark, Rosé Pine, and Solarized Dark and Light (Chroma has a style for each).
- **One format, embedded and user-defined:** each built-in is a TOML file in the format `~/.rho/themes/` already uses, embedded in the binary. A user theme can start from any built-in (`base = "nord"`) and override some roles.
- **One setting for every frontend:** the GUI reads the same `theme` and maps the roles onto its CSS variables and its highlight.js colors (or switches to Chroma's HTML output for code, so the colors are exactly the same). "System" stays available in the GUI as a pair of themes, one for light and one for dark. If sharing turns out to be awkward, the TUI and GUI settings can be split.
- **Export:** the page uses the current theme's colors and highlights code with Chroma's HTML formatter.
- **Picking:** `/theme` lists the built-in and user themes in both frontends, and the choice applies at once.
- **Checks:** warn on an unknown theme or Chroma style name instead of falling back silently. Document the theme format and the role names.

### Not taken

- **bubbletint** (`github.com/lrstanley/bubbletint/v2`): over 770 schemes from iTerm2-Color-Schemes, with Chroma and Glamour mappings and following the terminal's theme. Its schemes are terminal palettes (the 16 ANSI colors plus foreground and background), so every Rho role would still be derived automatically, and 770 unchecked themes would vary in quality. It could come back later for "match my terminal".

## Other ideas from the same review

Mentioned while comparing Rho with other harnesses and editors, not requested yet:

- **Images:** paste or drop a screenshot into the input for models with `supports_images`. In the TUI, paste an image path or read an image from the clipboard.
- **Edit an earlier prompt and send it again** in one step, instead of `/rewind` and retyping.
- **Custom key bindings in the TUI:** `tui.keybindings` is read but not applied yet.
- **GUI command palette** (Ctrl+K).
