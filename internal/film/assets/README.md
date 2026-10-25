# Film type assets

The film display face is rasterized from **Barlow Condensed ExtraBold** and
its supporting face from **Barlow Medium**, Copyright 2017 The Barlow Project
Authors. The derived PNG glyph atlases and metrics are distributed under the
[SIL Open Font License 1.1](OFL.txt), separate from the engine's MIT license.
No proprietary system font or retail asset is included.

Primary upstream: [The Barlow Project](https://github.com/jpt/barlow).
Distribution source: Google Fonts, revision
`f2bd09badbc763d8757951d52deec29da27e85fb`:

- [BarlowCondensed-ExtraBold.ttf](https://github.com/google/fonts/blob/f2bd09badbc763d8757951d52deec29da27e85fb/ofl/barlowcondensed/BarlowCondensed-ExtraBold.ttf),
  SHA-256 `724c9c25952d5f4a2d87185d9767aa006144c5f0d944dc05bf7d5d603551c260`.
- [Barlow-Medium.ttf](https://github.com/google/fonts/blob/f2bd09badbc763d8757951d52deec29da27e85fb/ofl/barlow/Barlow-Medium.ttf),
  SHA-256 `f8906f762cb73dca441da034bc363b2d8e2e68bc10d5c05e58717646c20cc4b4`.
- [Original license](https://github.com/google/fonts/blob/f2bd09badbc763d8757951d52deec29da27e85fb/ofl/barlow/OFL.txt).

To reproduce, download these exact TTFs outside the checkout, verify the
checksums and run `python3 tools/film-font DISPLAY.ttf BODY.ttf` with Pillow
12.3.0 and FreeType 2.14.3. The generator records glyph bounding boxes,
advances and kerning at 256-pixel em size. The cap height comes from the H;
text sizes remain cap heights. Runtime uses filtered coverage mipmaps for
small type and the full atlas for large headlines. No font parsing library
or Python runtime is needed to capture a film.

Supported characters: printable ASCII, en/em dashes, typographic quotes,
multiplication sign and ellipsis; and basic Cyrillic with Ґґ Єє Іі Її Ёё,
the Ukrainian apostrophe ʼ, № and « » (below). Both faces preserve lower
case. Unsupported
characters retain a space's advance. `display` is the default for titles and
lower thirds; `body` is the default for subtitles and captions.

## Cyrillic

Barlow has no Cyrillic. `tools/film-font-cyrillic` appends the Cyrillic
glyphs, rasterized from **Fira Sans Extra Condensed ExtraBold** for `display`
and **Fira Sans Regular** for `body` (Copyright 2012-2015 The Mozilla
Foundation and Telefonica S.A., SIL Open Font License 1.1, see
[OFL.txt](OFL.txt)), each scaled so its cap height matches Barlow's. The
Latin glyphs above are kept byte for byte: the script crops each atlas back
to them and lays the Cyrillic glyphs out below, and only Cyrillic pairs are
kerned. Distribution source: Google Fonts, branch `main` at
`7085eb89a950e85db5b166b7a58d414544b4140c`:

- `ofl/firasansextracondensed/FiraSansExtraCondensed-ExtraBold.ttf`,
  SHA-256 `056cad28b3697bf0a093eb49e55d02e873b1de1eb4ed64467c8c80b10c6750e9`.
- `ofl/firasans/FiraSans-Regular.ttf`,
  SHA-256 `c29556a2719bf613ef3d5e070e40d903a8965d9c081beca1375dc1e6e0f93c23`.

To reproduce, download them outside the checkout, verify the checksums and
run `python3 tools/film-font-cyrillic DISPLAY.ttf BODY.ttf` (rasterized with
Pillow 10.2.0 and FreeType 2.13.2).
