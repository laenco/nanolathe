# Interface translation

Nanolathe's own interface can be read in another language than English;
Ukrainian is the first (issue #144, user-authorized 2026-10-21). This
document is the design.

## Catalogs

`internal/i18n` holds one catalog per language, `locales/<code>.json`,
embedded in the binary. A catalog maps the English text the interface already
draws to its translation, the way retail's `translate.tdf` keys its sections
by the English words. There are no identifiers to keep in step with the
code:

- text with no entry is drawn as written, so a new or reworded string stays
  readable in English until the catalog catches up;
- an entry whose English no longer appears is harmless.

A key containing `{}` or `{#}` is a pattern. `{}` matches a run of text and
`{#}` a number; the translation places the runs at its own `{}` in order, or
at `{1}`, `{2}`… by number when the language orders them differently. A run a
pattern captures is translated too (two levels deep), so a sentence assembled
from translated parts, such as an action label inside a description, reads as
one. Longer keys are tried first, so a specific sentence wins over a general
one. Exact strings are looked up directly; pattern results are kept in a
bounded cache.

English is the source language and has no catalog. `SetLanguage` selects a
catalog; one that fails to load leaves English with a notice.

## Where text is translated

Translation is presentation only. Nothing the simulation, saves or replays
compute reads it.

The Nanolathe screen sets all its text through `internal/platform/screenkit`.
`Font.Measure`, `Font.Draw` and `Font.Wrap` translate a run before measuring
or setting it, so the layout measures what is drawn. Several kinds of text
set `Style.Verbatim` instead:

- text the player typed;
- keyboard legends;
- lines `Wrap` already translated;
- the screen's kept capitals (`nlUICache.upperCase`, translated once and
  then kept);
- a fitted line (`nlUICache.fit` translates before it shrinks or elides, so
  eliding never leaves an English fragment no entry matches);
- the unit viewer, which measures on the CPU without translation.

The kept capitals, wrapped lines and fitted lines start over when the
language changes.

Where a translation runs longer than the English the screen was laid out for,
the layout gives way:

- a card's value line shrinks to the room left of its lamps;
- the "What it changes" list starts its second column past the first
  column's widest line.

## Fonts

The film faces (Barlow) have no Cyrillic. `tools/film-font-cyrillic` appends
Cyrillic glyphs from Fira Sans to the atlases, scaled to Barlow's cap height,
and leaves the Latin glyphs byte for byte; `internal/film/assets/README.md`
records the sources.

## Choosing the language

- The `language` setting holds a language code, or nothing for English.
- `--language <code>` overrides it for one run.
- The Language card on the Game page changes the setting on Apply. Each
  language is named and described in its own words, so a player who cannot
  read the running language can still find theirs.

## Adding a language

1. Add `internal/i18n/locales/<code>.json`, mapping the screen's English to
   the language. A partial catalog works: missing entries stay English.
2. Add the code and the language's own name for itself to `i18n.Languages`.
3. If the language needs letters the film faces lack, append them to the
   atlases as `tools/film-font-cyrillic` does for Cyrillic.

## Scope

Text Nanolathe draws with `screenkit` is translated wherever the catalog
has an entry for it, and the Ukrainian catalog covers the Nanolathe screen.
The unit viewer's text is drawn Verbatim.

These stay as the game's files have them:
- the menus drawn in the original game's art;
- the battle interface;
- unit names and descriptions;
- mission briefings.

Translating them needs glyphs in the game's GAF/FNT fonts, and the text
retail draws from `translate.tdf` and the GUI files. That is not part of
this change.
