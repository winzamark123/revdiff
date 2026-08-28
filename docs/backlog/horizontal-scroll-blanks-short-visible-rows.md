---
worth: maybe
where: app/ui/diffview.go:166
added: 2026-08-28
---
# horizontal scroll blanks short visible rows with no « indicator

`maxHorizontalScroll` bounds `scrollX` against the widest row in the rendered document, so a wide row
anywhere in the file sets the ceiling for the whole file. Scroll toward that ceiling while the viewport
shows only short rows and every visible row cuts to nothing: both overflow flags in
`applyHorizontalScroll` go false (`origWidth > start` fails because the row is short, `origWidth > end`
likewise), so it falls to the plain-cut branch, `ansi.Cut` returns empty, and no `«` is drawn to say the
content is off to the left.

Concretely: line 900 is a 5000-column minified blob, lines 1-40 are ordinary 40-column code, pane width
100. The bound is ~4900. With the cursor near the top, scrolling right stops at 4900 rather than running
away, but the visible pane stays blank until the offset is reduced or a wider row enters the viewport.

This predates #337, which removed the unbounded case where the offset could exceed every row in the
file. The narrower case remains. The per-line behavior is `applyHorizontalScroll`'s and is
pinned by `TestModel_ApplyHorizontalScrollNoLeftIndicatorWhenScrolledPastContent`
(`app/ui/diffview_test.go`).

`maybe` because resolving it needs a product choice. A per-viewport bound would keep the pane populated
but make the offset jump as you scroll vertically, breaking column alignment between rows, which is what
a document-wide offset buys. Drawing a `«` on a row scrolled past its own end is the smaller
alternative and would require changing the behavior pinned by that test. Surfaced by the review of
#337, which came out of #334.
