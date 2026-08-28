# revdiff

A terminal UI for reviewing diffs, files and documents with inline annotations, built on Bubble Tea.
Annotations go to stdout on quit, so the usual caller is an AI coding agent that launches revdiff,
reads back what the reviewer marked, and acts on it. Distributed via Homebrew and `go install`, plus
plugins for Claude Code, Codex and Pi that launch it into a terminal overlay.

## What a real failure looks like

A reviewer loses work, or is misled about the code in front of them. In order of seriousness:

- integrity of the review itself. Annotations lost or written wrong, since they are the reviewer's
  actual output and the only thing the calling agent sees, so silent truncation of the `-o` file is
  worse than a visible error. In the same class: a diff rendered wrong, a line attributed to the
  wrong file, a rename shown as delete-and-add, a hunk boundary off by one. The reviewer then
  annotates content that is not there, and nothing on screen says so.
- the TUI wedged or unrecoverable. Bubble Tea's event loop is single-threaded, so a walk that never
  terminates inside `Update` cannot be quit from inside the app and leaves the terminal in raw mode.
  Visible, unlike the class above, but it takes the session's unsaved work with it.
- a pane that goes blank, or content no key can reach.
- style and layout damage: bleeding backgrounds, broken column alignment, a wrapped header.

## Blast radius

One local review session. No remote service, no shared or customer data, nothing deployed. The state
at risk is the current annotations plus the optional `-o` output snapshot. A normal quit and the
handled signals (SIGHUP, SIGTERM) save history to `~/.config/revdiff/history/`; a hard-killed wedge
(`kill -9`, `SIGQUIT`) finalizes nothing, so what survives it is an earlier `O` flush or a completed
history save and nothing else.

## Who runs and maintains it

Solo maintainer (umputun), no on-call. Outside contributors send PRs. Changes are reviewed before
merge and released on the maintainer's own schedule.

## Reporting bar

Calibrate to a personal developer tool rather than a production service. Worth reporting:

- anything in the list above, at its listed seriousness
- deviation from a documented rule in `CLAUDE.md` or `.claude/rules/gotchas.md`. Those files record
  decisions that were paid for, and re-deriving one is the expensive mistake
- a new code path with no test, or a test that would pass with the change reverted
- an exported identifier with no out-of-package caller

Three classes are noise only once their condition is checked, so check it rather than assuming:

- a concurrency claim, once every access to the state is verified to be owned by the Bubble Tea
  event loop
- malformed-input hardening, once every supported entry path rejects the input. `--stdin`, standalone
  file mode and `--compare-old/--compare-new` all read data the VCS never produced, so "the VCS
  cannot emit this" does not bound what reaches the app
- a performance claim, once it names a realistic input and a measurement. The repo benchmarks at 10k
  and 50k lines and #179 was a real user-reported stall, so diff size alone does not make it
  hypothetical

## Deliberate conventions, not defects

These look wrong and are not. Read `CLAUDE.md` and `.claude/rules/gotchas.md` before flagging any:

- specific ANSI resets (`\033[39m`, `\033[22m`) rather than `\033[0m`, to preserve lipgloss
  backgrounds; raw ANSI rather than nested `lipgloss.Render()` for inline styled spans
- `Model` is copied by value, and where state must persist across copies it is held in pointer- or
  reference-backed storage deliberately (`renderCache`, `annot.rowCache`)
- British spelling in the chroma API (`Colour`), suppressed with `//nolint:misspell`
- private by default for functions, methods, types and fields; a function called only from one
  struct's methods is a method of that struct
- vendored dependencies, so `go mod vendor` follows any dependency change

The parallel slices on `loadedFileState` (`lines`, `highlighted`, `intraRanges`, `lineWidths`) are
also intentional, but they are not thereby safe: every mutation must preserve length and content
alignment, and a derived cache must recompute or heal as its own documentation says. A missed
synchronized update there is a real defect, which is how `lineWidths` was found.

## Bar for a fix

Match the fix to the finding: a minor defect gets a minor fix, and a nit never justifies refactoring
the code around it. Prefer the change whose blast radius is smaller than the defect's. A proposal to
add an abstraction, a package or a layer has to argue for itself against not doing it.
