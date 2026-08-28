---
worth: later
where: app/ui/diffnav.go:583
added: 2026-08-20
---
# hunk-nav comments claim delete-only hunks are skipped, but they are not

`moveToNextHunk` (`app/ui/diffnav.go:583`) and `moveToPrevHunk` (`:599`) both guard
`firstVisibleInHunk(...) < 0` with `// skip delete-only hunks in collapsed mode`. That is not what
happens. `isCollapsedHidden` (`app/ui/collapsed.go:415`) deliberately keeps a delete-only hunk's first
removed line visible as the `⋯ N lines deleted` placeholder, so `firstVisibleInHunk` returns
`hunkStart` for such a hunk and `]` lands on it. A mixed hunk always has a visible add, so it returns
that. The `< 0` branch looks unreachable for any real hunk, which means both comments describe a case
that does not occur and name the wrong reason for a guard that does.

The godoc added for `positionOnFirstChange` (`:906`) states the opposite and correct behavior, so the
package now contradicts itself in three places. Surfaced by the revmux review of PR #329 and confirmed
by codex, which also pinned the real behavior in `TestModel_CollapsedHunkNavigationDeleteOnly`.

Deferred rather than fixed inline because #329 was scoped to the `--start-at-change` flag and these two
lines predate it. The fix is either correcting both comments to say what the guard is for, or
establishing whether `< 0` is genuinely unreachable and dropping the branch. Worth deciding which
before editing, since a comment that keeps a dead branch alive is the weaker of the two outcomes.
