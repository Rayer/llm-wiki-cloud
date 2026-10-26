# Local fonts

These files are fixed build inputs. The app imports them with next/font/local, so builds do not download fonts.

| Family | Pinned source | License | Asset |
| --- | --- | --- | --- |
| Geist | Vercel Geist release v1.7.2, fonts/Geist/webfonts/Geist[wght].woff2 | SIL Open Font License 1.1, OFL-Geist.txt | 69,760 bytes; SHA-256 2ffebe993e969069a9789d15164b7715d42491b5835516c5e3b935d5f81b05f1 |
| Geist Mono | Vercel Geist release v1.7.2, fonts/GeistMono/webfonts/GeistMono[wght].woff2 | SIL Open Font License 1.1, OFL-Geist.txt | 71,596 bytes; SHA-256 afaacc4c5fbba89d2ebf7a02dc4070208540874592a5504d57175782fe893101 |
| Noto Serif TC | Google Fonts ofl/notoseriftc snapshot at commit 6d17dab13b85129360f9748f057c7f67c5f484d4; upstream Noto CJK source commit 985fa52c81c1d6692ccdd82bc3656e8fb932fd89 | SIL Open Font License 1.1, OFL-NotoSerifTC.txt | 16,851,596 bytes; SHA-256 0077e18f57c6908f4a000969880940bdb0dad057c0e8d98b49dc364c3d1b09c6 |

Sources: [Geist v1.7.2](https://github.com/vercel/geist-font/tree/v1.7.2), [Google Fonts Noto Serif TC snapshot](https://github.com/google/fonts/tree/6d17dab13b85129360f9748f057c7f67c5f484d4/ofl/notoseriftc), and its [Noto CJK upstream](https://github.com/notofonts/noto-cjk/tree/985fa52c81c1d6692ccdd82bc3656e8fb932fd89).

Noto Serif TC is the variable font and includes Traditional Chinese glyphs; the existing design uses weights 500 and 600. The font is intentionally a single full language file rather than the Latin-only subset previously configured.
