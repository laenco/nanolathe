# Evolva model packages

## Scope and sources

This reference describes authored model assets, not retail engine behavior.
The source artifacts are the following maintainer-supplied ZIPs, inspected on
2026-10-19. Their display filenames do not by themselves establish official
release numbering or authorship of every later adaptation.

| ZIP | Bytes | SHA-256 |
|---|---:|---|
| `Evolva_UH40_v1.zip` | 596623 | `53db9f4a33725f841484f7b705e4ced14b4937a34328fa60a27b498491615e0a` |
| `evolva-2004.zip` | 100499 | `39821c62645acd42510a1d9cf362502692a336b79c3ffd02a7cd28f060f8cc7f` |
| `evolva-2003.zip` | 470843 | `db0b5b6ddd28d250a822849de6f6dd32da2fa8c7889a7547912f8aeaaeb436d2` |
| `evolva-2002.zip` | 496507 | `76ea28b41750601f784c8ff104044c4fa61de04146623ba1b69410b45ca6e6d2` |

**Established (authored content).** Every non-directory member of each ZIP
is a model under `Objects3D/`. None includes COB/BOS scripts, unit
definitions, weapon definitions or texture files. The 2002, 2003, 2004 and
UH40 ZIPs respectively hold 114, 146, 28 and 174 model files. Every filename
in the 2004 ZIP also appears in 2003; that supplied ZIP alone is a partial
set. The separately downloaded `(1)` copies of the three dated ZIPs are
byte-identical to their unnumbered counterparts.

**Established (primary project documentation).** TAAN's
[Evolva page, updated 18 January 2004](https://web.archive.org/web/20040325023326id_/http://www.planetannihilation.com:80/taan/pages/evolva_systeme/taan_evolva.htm)
describes visually restyling existing units and preserving their recognizable
form. It credits the 3D/3DO model work to Sinclaire and the project idea to
Sinclaire and M3g. Installation creates the game's `Objects3D` directory and
extracts the model pack there; ordinary solo and network play is documented.
No script installation is prescribed. The earlier
[beta 4.1 page, dated 7 October 2003](https://web.archive.org/web/20031106185502id_/http://www.planetannihilation.com:80/taan/pages/evolva_systeme/taan_evolva.htm)
likewise prescribes model-only installation.

**Supported inference.** These historical instructions intend to retain the
installation's existing scripts. They do not certify every supplied ZIP's
compatibility with every script set. The archive catalogue separately lists
[ordinary Evolva and Evolva Uberhack models](https://files.tauniverse.com/files/ta/other/),
supporting an UberHack adaptation, but it does not establish the exact UH40
artifact's author-certified release contract.

## Nanolathe compilation policy

**Nanolathe content policy, user-authorized 2026-10-19.** The add-on is named
**Evolva models**, id `evolva-models`, version `1`. For each canonical model
filename it takes the first source supplying it, in UH40, 2004, 2003, 2002
order. It fills gaps only and preserves the selected model bytes. This is a
Nanolathe compilation, not a claimed original Evolva release.

**Established (bounded asset comparison).** That order selects 174 files
from UH40, two from 2004, five from 2003 and one from 2002: 182 model files,
97 without `_dead` in their names and 85 with it. The four extra unit-model
filenames beyond UH40's set are `armflea`, `armmex`, `armmlv` and `armrock`.
Ten unit-model names in the combined set have no corresponding unit in the
reference original installation. Supplying their models creates no units.
These counts characterize the pinned sources, not a general format limit.

The implementation and tests belong to DESIGN_MODS_MUTATORS §5.7:
model-only content mounts after the base game, Cavedog downloads and fix
add-ons, and before factions and the selected mod. Its model bytes use the
existing model loader and existing scripts; it selects no gameplay rule.
`MODEL-SOURCES.txt` in the package records the selected source for every file.

## Unknowns

- Exact original download URLs and release notes for the four supplied ZIPs
  were not recovered. Their byte identities above settle which inputs the
  compilation uses.
- The exact intended UberHack script version for `Evolva_UH40_v1.zip` is
  unknown. An original readme or author release announcement would settle it.
  No replacement scripts are inferred or synthesized.

The missing readme does not block the explicitly authorized asset compilation.
Retail visibility of children of hidden model pieces is independently
established in [03 §2.4.1]; the models do not authorize a new rendering rule.
