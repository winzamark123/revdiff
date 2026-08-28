---
worth: later
where: app/ui/sidepane/filetree.go:25
added: 2026-08-20
---
# reviewed marks do not survive the process

Reviewed marks (`Space`) live only in `FileTree.reviewed` and are lost on exit, so `F` (unreviewed-only)
is useful within one session and worthless across two. Nothing else preserves them: `history.Save`
returns early when there are no annotations (`app/history/history.go:43`), so a session that produced
marks and no annotations writes nothing at all. Requested in #324, which asked for `--reviewed-output`
to write bare paths and `--reviewed` to preload them. The need was accepted publicly; the bare-path
format was not.

Constraints settled while answering #324, so they do not have to be re-derived:

- **The file must carry the fingerprint, not just the path.** A mark is `path -> semantic diff
  fingerprint`, and `SetReviewed` refuses an empty fingerprint. Preloading bare paths either gets
  dropped by the first `ReconcileReviewed` or silently adopts today's fingerprint, which makes `F` hide
  files whose content was never read. That is the defect the fingerprint mechanism was added to prevent.
- **Structured and versioned, not line-oriented.** Paths may contain tabs and newlines; revdiff already
  strips control bytes before display (`app/ui/style/display.go:17`), so a delimited format cannot
  round-trip a hostile or merely unusual path.
- **Import and export stay separate.** GitHub's `viewerViewedState` returns paths with no fingerprint, so
  an inbound GitHub list can only ever be advisory and has to be documented as such.
- **Export belongs inside the `!signaled` gate, next to `-o`.** `finalize` returns `(0, nil)` on a signal
  (`app/main.go:314`), so a checkpoint written on SIGTERM is indistinguishable to a calling script from a
  finished review and would publish a partial set as complete. It must, however, sit outside the
  `annotations == ""` guard at `app/main.go:310`, or the marks-only case #324 is about still writes
  nothing.
- **No ref-range equality gate on the file.** It would discard marks after a rebase, which is exactly the
  case `FileFingerprint` deliberately preserves.

Open decisions, both the maintainer's:

- does `Q` suppress the export? `Q` is documented as discarding annotations and a reviewed mark is not an
  annotation, but pressing `Q` usually means the session was a write-off.
- does an empty reviewed set write an empty snapshot or leave the previous file? Writing it is safer
  (stale marks cannot resurrect), leaving it is friendlier to a mistyped path.

Cost worth knowing before starting: preloaded marks are validated by the existing pipeline, but
`loadReviewedFingerprints` runs inside `loadFiles` before `filesLoadedMsg` is emitted, so each preloaded
path costs one effective-diff fetch through the 4-worker pool before the file list paints. Measure that
for a large preload rather than assuming it is free.
