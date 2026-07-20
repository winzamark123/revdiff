# pi integration

This directory contains the **pi-specific** integration for revdiff.

## Contents

- `extensions/revdiff.ts` — registers the user-only `/diff` command, opens revdiff in a right-side tmux pane, and sends flushed annotations to pi
- `scripts/detect-ref.sh` — packaged smart-target detection script used by the extension

The integration does not register a model tool or skill. Only an explicit `/diff` command can launch revdiff, and revdiff is not reopened after pi handles annotations.

## Install

From the repo root:

```bash
pi install https://github.com/umputun/revdiff
```

The root `package.json` includes the extension and detection script under `plugins/pi/`.

## Usage

```text
/diff [args]
```

With no arguments, `/diff` detects uncommitted, staged, or branch changes. Arguments are passed through as revdiff arguments after shell-like splitting.

```text
/diff
/diff HEAD~1
/diff main
/diff --staged
/diff --untracked
/diff --all-files
/diff --all-files --exclude vendor
/diff --only README.md
/diff HEAD~3 --description="why this refactor matters"
/diff HEAD~3 --description-file=/tmp/revdiff-desc.md
/diff main --annotations=/tmp/revdiff-review.md
```

Press `O` in revdiff to send the current annotation snapshot to pi without closing the review. After pi edits the code, press `R` to reload the diff. Quitting revdiff sends any final snapshot that was not already delivered. Running `/diff` while the review pane is open focuses that pane instead of starting another review.

The model cannot launch or reopen revdiff; only the user can start or focus it with `/diff`.

## Notes

- Requires pi to run inside tmux
- Requires the `revdiff` binary on `PATH`
- Set `REVDIFF_BIN=/absolute/path/to/revdiff` if pi cannot find the binary
- Line numbers are enabled by default; set `REVDIFF_LINE_NUMBERS=false` before starting pi to disable them
- The review opens in a 50% pane to the right of pi
- Exit code `10` means annotations were captured, not failure
- Use `--untracked` when new untracked files should be included
- Use `--description` or `--description-file` so the info popup carries review context
- Use `--annotations=<tempfile>` to preload review notes

This integration is intentionally kept separate from other harnesses:

- Claude Code integration lives in `.claude-plugin/`
- OpenCode integration lives in `plugins/opencode/`
- pi integration lives here in `plugins/pi/`
