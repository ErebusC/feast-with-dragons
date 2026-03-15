# feast-with-dragons

A tool for building combined epub and audiobook files from A Feast for Crows and A Dance with Dragons, interleaved in a custom reading order. Three built-in reading orders are provided, and the tool supports fully custom orders via JSON config files. Both ebook and audio output are supported.

This tool was developed with AI assistance.

---

## Requirements

### Ebook output

- Go 1.22 or later
- A Feast for Crows epub (Bantam edition)
- A Dance with Dragons epub (split-file edition)

### Audio output

All of the above, plus:

- ffmpeg and ffprobe on PATH
- Audio files for each book in mp3, m4b, m4a, flac, ogg, or opus format

---

## Building

```
go build -o feast-with-dragons .
```

The binary embeds all three built-in configs at compile time. No additional files are required at runtime.

---

## Usage

```
feast-with-dragons <subcommand> [flags]
```

If no subcommand is given, `ebook` is assumed.

### Subcommands

| Subcommand | Description |
|---|---|
| `ebook` | Build a spliced epub from source epub files |
| `audio` | Build a spliced M4B from source audio files |
| `merge` | Concatenate whole books without chapter-level splicing |
| `scan` | Print the spine of an epub, or generate a skeleton config with `-init` |
| `validate` | Dry-run a config against source epubs without producing output |
| `diff` | Compare two splicings and show chapter differences |

### Common flags

The `ebook`, `audio`, and `merge` subcommands accept the following flags:

| Flag | Description |
|---|---|
| `-quiet` | Suppress progress output |
| `-force` | Overwrite an existing output file instead of refusing |

---

## ebook

Builds a spliced epub by extracting chapters from the source epubs and writing them in the order defined by the active splicing config.

```
feast-with-dragons ebook [flags]
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-splicing` | `fwd` | Splicing to use: `fwd`, `boiled`, `ball`, or a path to a custom JSON config |
| `-affc` | auto-detected | Path to the A Feast for Crows epub |
| `-adwd` | auto-detected | Path to the A Dance with Dragons epub |
| `-book id=path` | | Source epub for a custom book ID; repeatable |
| `-out` | `<splicing name>.epub` | Output file path |
| `-annotate` | off | Add a small source book annotation (e.g. `[AFFC]`) to the bottom of each chapter |
| `-numbered-toc` | off | Prepend chapter numbers to the table of contents |
| `-quiet` | off | Suppress progress output |
| `-force` | off | Overwrite an existing output file |

If `-affc` or `-adwd` are not provided, the tool searches the current directory for epub files whose names contain both `feast` and `crows`, or both `dance` and `dragons`, respectively. Both keywords must be present to match, so an output file named `A Feast with Dragons.epub` will not be falsely detected as the AFFC source.

If the output file already exists the tool refuses to proceed unless `-force` is set.

### Cover image

The cover is resolved in this order:

1. A file named `cover.jpg` in the current directory, if present.
2. A generated horizontal fade blend of the two source book covers. Both JPEG and PNG cover images are accepted as input; the output is always JPEG.
3. If only one source book has a cover, that cover is used alone.

### Examples

```
feast-with-dragons ebook
feast-with-dragons ebook -splicing boiled
feast-with-dragons ebook -affc ./my-affc.epub -adwd ./my-adwd.epub -out "My Order.epub"
feast-with-dragons ebook -splicing ./my-config.json -book AFFC=./affc.epub -book ADWD=./adwd.epub
feast-with-dragons ebook -annotate -force -out reread.epub
```

---

## audio

Builds a spliced M4B by extracting chapter segments from source audio files and concatenating them in the order defined by the active splicing config. Chapter metadata is embedded in the output file.

Requires ffmpeg and ffprobe on PATH.

```
feast-with-dragons audio [flags]
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-splicing` | `fwd` | Splicing to use: `fwd`, `boiled`, `ball`, or a path to a custom JSON config |
| `-affc` | auto-detected | File or directory containing AFFC audio |
| `-adwd` | auto-detected | File or directory containing ADWD audio |
| `-book id=path` | | Audio file or directory for a custom book ID; repeatable |
| `-out` | `<splicing name>.m4b` | Output file path |
| `-quiet` | off | Suppress progress output |
| `-force` | off | Overwrite an existing output file |

If `-affc` or `-adwd` are not provided, the tool searches the current directory for audio files whose names contain both `feast` and `crows`, or both `dance` and `dragons`, respectively. Both keywords must be present to match. Multi-part audiobooks are supported: all matching files in the directory are collected and probed in filename order.

### Chapter matching

Chapter assignment is positional. The Nth chapter in the config maps to the Nth chapter segment found in the audio source after filtering. The title from the config is compared against the audio metadata title as a sanity check only; a mismatch produces a NOTE in the output but does not stop the build. This is expected for AFFC, which labels chapters numerically in some editions.

Chapters are filtered before assignment. Segments shorter than 30 seconds are dropped. Segments whose lowercase title is `intro` or `credits` are also dropped.

Consecutive segments with the same title, including segments that span multiple audio files, are merged into a single logical chapter before assignment. This handles editions that split a chapter across tracks or across parts of a multi-part audiobook.

The build uses a two-pass approach. In the first pass, each chapter segment is extracted to a temporary file with timestamps reset to zero. In the second pass, the temporary files are concatenated and chapter metadata is written. Temporary files are deleted on exit.

### Examples

```
feast-with-dragons audio
feast-with-dragons audio -affc ./affc.m4b -adwd ./adwd-part1.m4b
feast-with-dragons audio -splicing boiled -out "Boiled Leather.m4b"
```

---

## merge

Concatenates two or more epub or audio files into a single output file without chapter-level splicing. For audio, chapter markers from each source file are preserved and their timestamps adjusted to be contiguous in the output.

The output format is determined by the file extension of the `-out` argument.

```
feast-with-dragons merge -out <file> [flags] file1 file2 ...
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-out` | required | Output file path; extension determines format (`.epub`, `.m4b`, `.m4a`, `.mp3`) |
| `-title` | `Merged` | Title written into output metadata |
| `-author` | | Author name written into output metadata |
| `-quiet` | off | Suppress progress output |
| `-force` | off | Overwrite an existing output file |

### Examples

```
feast-with-dragons merge -out combined.m4b -title "ASOIAF Books 4-5" affc.m4b adwd.m4b
feast-with-dragons merge -out combined.epub -title "Combined" book1.epub book2.epub
```

---

## scan

Prints the spine of an epub file in order, showing the zip-internal path and a best-effort title for each entry. Useful when writing a custom config for an epub edition you have not used before.

```
feast-with-dragons scan [flags] <epub>
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-init` | | Write a skeleton JSON config to this path instead of printing the spine |
| `-id` | `MYBOOK` | Book ID to use in the generated config (only with `-init`) |
| `-json` | off | Output the spine as a JSON array instead of a table |

When `-init` is given, the tool reads the epub's spine and NCX and writes a JSON config file with one chapter entry per spine item. Titles are taken from the NCX labels where available, falling back to a text snippet from the page content. The generated config has `auto_detect` and `use_spine` enabled for the book. Edit the output to reorder, remove, or relabel chapters as needed.

When `-json` is given, the spine is printed as a JSON array with `num`, `file`, `ncx_title`, and `snippet` fields per entry. Useful for scripting or piping into other tools.

### Examples

```
feast-with-dragons scan mybook.epub
feast-with-dragons scan -init myconfig.json -id GOT mybook.epub
feast-with-dragons scan -json mybook.epub
```

---

## validate

Dry-runs a config against source epubs without producing any output. Reports missing chapters, duplicate entries, missing source epubs, and out-of-range spine references. Useful for checking a custom config before committing to a full build.

```
feast-with-dragons validate [flags]
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-splicing` | `fwd` | Splicing to validate |
| `-affc` | auto-detected | Path to the A Feast for Crows epub |
| `-adwd` | auto-detected | Path to the A Dance with Dragons epub |
| `-book id=path` | | Source epub for a custom book ID; repeatable |

### Examples

```
feast-with-dragons validate
feast-with-dragons validate -splicing ./my-config.json -book MYBOOK=./mybook.epub
```

---

## diff

Compares two splicings and shows which chapters are unique to each, and which appear in both but at different positions. Accepts built-in splicing names or paths to custom JSON configs.

```
feast-with-dragons diff <splicing-a> <splicing-b>
```

### Examples

```
feast-with-dragons diff fwd boiled
feast-with-dragons diff fwd ./my-custom-order.json
```

---

## Built-in splicings

Three reading orders are embedded in the binary.

### fwd -- A Feast with Dragons

The most widely recommended interleaved reading order. The two prologues are read first, then chapters from both books are interleaved chronologically. Samwell I and Jon II appear as separate chapters. The Melisandre chapter is titled "Melisandre I" and the Victarion chapter near the end is titled "Victarion I". Produces 119 chapters.

Selected with `-splicing fwd` or `-splicing feast-with-dragons`.

### boiled -- Boiled Leather

A reading order based on the Boiled Leather blog's recommendation. Broadly similar to fwd but with some reordering of chapters in the middle section. Samwell I and Jon II appear as separate chapters. Produces 119 chapters.

Selected with `-splicing boiled` or `-splicing boiled-leather`.

### ball -- A Ball of Beasts

A variation that combines Samwell I and Jon II into a single chapter. The Melisandre chapter is titled "The Red Priestess" and the Victarion chapter near the end is titled "The One of Two Gods". Produces 118 chapters.

Selected with `-splicing ball` or `-splicing ball-of-beasts`.

---

## Custom configs

Any JSON file can be passed to `-splicing`. The schema is as follows.

### Top-level fields

| Field | Required | Description |
|---|---|---|
| `name` | yes | Title of the output book, used for the epub title and default output filename |
| `author` | no | Author name written into epub and audio metadata. Defaults to `George R. R. Martin` for epub and `Unknown` for audio if omitted |
| `series` | no | Series name shown on the generated cover page. Defaults to `A Song of Ice and Fire` if omitted |
| `books` | no | Per-book extraction config. If omitted, built-in defaults for AFFC and ADWD are used |
| `front_matter` | yes | Array of front matter pages to include before the table of contents |
| `chapters` | yes | Array of chapter entries in the desired reading order |

### books

Each key is a book ID string. Values are objects with the following fields, all optional:

| Field | Description |
|---|---|
| `css_src` | Zip-internal path of the book's stylesheet |
| `css_dest` | Filename to write to `OEBPS/Styles/` in the output epub |
| `image_src_prefix` | Zip path prefix identifying image files belonging to this book |
| `image_dest` | Subdirectory name under `OEBPS/Images/` in the output epub |
| `cover_src` | Zip-internal path of the book's cover image (JPEG or PNG) |
| `chapter_paths` | Explicit list of zip-internal paths for each chapter in order. Chapter N maps to index N-1 |
| `chapter_template` | A `fmt.Sprintf` template receiving the chapter number, used when `chapter_paths` is absent. Example: `"OEBPS/Text/chapter_%03d.html"` |
| `strip_toc_links` | Boolean. Removes dead anchor tags wrapping chapter title spans. Required for some epub editions |
| `expected_chapters` | Integer. Audio only. Used to produce a warning if the number of detected chapters does not match |
| `use_spine` | Boolean. Read the epub's OPF spine at build time and use it as the chapter path list. See below |
| `auto_detect` | Boolean. Derive CSS, cover, and image paths from the OPF manifest at build time. Does **not** imply `use_spine`. See below |

For book IDs `AFFC` and `ADWD`, built-in defaults are provided for all fields and match the specific epub editions listed under Requirements. Fields present in the `books` section override the built-in defaults; absent fields fall back to them. For any other book ID, all relevant fields must be provided explicitly.

If a config references a book ID that is not `AFFC`, `ADWD`, or listed in the `books` section, the tool prints a warning at load time. This catches typos early. The build still proceeds if the ID is supplied at runtime via `-book`.

### front_matter

Each entry describes a single page included before the table of contents:

| Field | Required | Description |
|---|---|---|
| `book` | yes | Book ID of the source epub |
| `file` | yes | Zip-internal path of the HTML file within that epub |
| `title` | yes | Label shown in the table of contents |

A small annotation is appended to each front matter page indicating which source book it came from.

### use_spine

When `use_spine` is set to `true`, the tool reads the epub's OPF spine at build time and uses it as the chapter path list. Chapter `num` N in the config maps to the Nth spine entry (1-indexed), adjusted by `spine_offset` if set.

Every well-formed epub exposes its content in spine order regardless of internal naming conventions, so this works with any edition. The trade-off is that spine entries include front matter, TOC pages, and other non-chapter content, so `num` values refer to spine position rather than narrative chapter number unless `spine_offset` is used.

`use_spine` is ignored for audio. It has no effect if `chapter_paths` is also set on the same book.

### spine_offset

`spine_offset` shifts the chapter number when resolving spine entries. Chapter `num` N maps to spine entry N + `spine_offset` (1-indexed after the shift). This lets you use narrative chapter numbers in the config without manually counting front matter items.

Run `scan` to find the correct offset. The scan output reports the index of the first NCX-labelled spine entry, which is typically where narrative chapters begin. Set `spine_offset` to that index value.

Example: if `scan` reports "first NCX-labelled spine entry is at index 7", set `spine_offset: 7`. Then `num: 1` maps to spine entry 8 (the prologue), `num: 2` maps to spine entry 9 (chapter one), and so on.

`spine_offset` is only applied when `use_spine` or `auto_detect` with `use_spine` is active.

### auto_detect

When `auto_detect` is set to `true`, the tool inspects the epub's OPF manifest and NCX (or EPUB3 nav document) at build time to fill in the following fields automatically:

- **`css_src`**: the first manifest item with `media-type="text/css"`
- **`cover_src`**: resolved via `<meta name="cover" content="item-id"/>` in the OPF metadata, with a fallback to scanning manifest items whose filename contains `cover` or `cvi`
- **`image_src_prefix`**: the longest common directory prefix among non-root image manifest items, excluding the cover
- **`css_dest`**: derived from the book ID as `<lowercase-id>.css`
- **`image_dest`**: derived from the book ID as `<lowercase-id>`
- **NCX index**: a title-to-path lookup table built from all NCX nav point labels, enabling title-based chapter resolution at build time

Any field already set explicitly in the config is left unchanged.

`auto_detect` does **not** imply `use_spine`. Chapter path resolution is a separate concern. For AFFC and ADWD, the built-in path functions remain active and `num` values work as documented. For unknown epub editions with no built-in path function, set both `auto_detect: true` and `use_spine: true`.

**Title-based chapter lookup**: when `auto_detect` is set and the NCX index is populated, chapter titles in the config are matched against NCX labels before falling back to num-based lookup. Two match strategies are tried in order:

1. Exact lowercase match: works for editions where the NCX includes full labels like "JON I" or "TYRION II".
2. Parenthetical-stripped match: "Prologue (Pate)" matches a "PROLOGUE" NCX entry. Only parentheticals are stripped -- roman numerals are preserved intentionally, so "Jon I" and "Jon II" do not collapse to the same entry.

If neither match succeeds the lookup falls back to num-based resolution, which uses SpineOffset, ChapterPaths, ChapterTemplate, or the built-in edition logic in that order.

The one field that cannot be auto-detected is `strip_toc_links`. If chapter titles in the output appear as dead links, add `"strip_toc_links": true` to the book config.

**EPUB3 support**: if the epub contains no NCX file, the tool looks for an EPUB3 nav document (a manifest item with `properties` containing `nav`) and parses it instead. The title-based lookup works the same way regardless of which format the epub uses.

Minimal custom config for a known edition (AFFC/ADWD) using `auto_detect`:

```json
{
  "name": "My Reading Order",
  "books": {
    "AFFC": { "auto_detect": true },
    "ADWD": { "auto_detect": true, "strip_toc_links": true }
  },
  "front_matter": [],
  "chapters": [
    {"title": "Prologue (Pate)",    "book": "AFFC", "num": 1},
    {"title": "Prologue (Varamyr)", "book": "ADWD", "num": 1},
    {"title": "Jon I",              "book": "ADWD", "num": 4}
  ]
}
```

Minimal config for an unknown epub edition:

```json
{
  "name": "My Reading Order",
  "books": {
    "MYBOOK": {
      "auto_detect": true,
      "use_spine": true,
      "spine_offset": 7
    }
  },
  "front_matter": [],
  "chapters": [
    {"title": "Chapter One", "book": "MYBOOK", "num": 1},
    {"title": "Chapter Two", "book": "MYBOOK", "num": 2}
  ]
}
```

Run `scan` first to find `spine_offset`. With `auto_detect`, if the NCX index matches your chapter titles exactly you can omit `spine_offset` entirely. Alternatively, use `scan -init` to generate a skeleton config automatically.

### chapters

Each entry is either a single chapter or a combined chapter.

A single chapter:

| Field | Required | Description |
|---|---|---|
| `title` | yes | Chapter title in the output table of contents |
| `book` | yes | Book ID of the source epub |
| `num` | yes | Chapter number within that book, 1-indexed |

A combined chapter (epub only) concatenates the body content of two or more source chapters into one output file, with a horizontal rule between parts:

| Field | Required | Description |
|---|---|---|
| `title` | yes | Chapter title in the output table of contents |
| `parts` | yes | Array of `{"book": "<id>", "num": <n>}` objects |

### Minimal custom config example

```json
{
  "name": "My Reading Order",
  "author": "George R. R. Martin",
  "front_matter": [
    {"book": "AFFC", "file": "OEBPS/Text/Mart_9780553900323_epub_ded_r1.htm", "title": "Dedication"}
  ],
  "chapters": [
    {"title": "Prologue (Pate)",    "book": "AFFC", "num": 1},
    {"title": "Prologue (Varamyr)", "book": "ADWD", "num": 1},
    {"title": "Jon I",              "book": "ADWD", "num": 4}
  ]
}
```

### Custom config with a third book

```json
{
  "name": "Extended Order",
  "books": {
    "MYBOOK": {
      "auto_detect": true,
      "use_spine": true,
      "spine_offset": 5
    }
  },
  "front_matter": [],
  "chapters": [
    {"title": "Chapter One", "book": "MYBOOK", "num": 1},
    {"title": "Chapter Two", "book": "AFFC",   "num": 1}
  ]
}
```

Pass a custom book source at runtime with `-book MYBOOK=./mybook.epub`. Run `scan` against the epub to find the correct `spine_offset`, or use `scan -init -id MYBOOK mybook.epub` to generate a starting config.

---

## Source file requirements

The built-in AFFC config targets the Bantam epub edition. Chapter paths follow the pattern `OEBPS/Text/Mart_9780553900323_epub_c##_r1.htm`, with the prologue at `Mart_9780553900323_epub_prl_r1.htm`. For a different edition, add `auto_detect: true` to detect assets automatically. If the NCX in your edition uses full chapter labels (e.g. "JON I", "CERSEI II"), title-based lookup will handle chapter resolution without needing num values to correspond to any particular offset. If the NCX uses POV-only labels without numerals (e.g. "JON", "CERSEI"), num-based fallback applies -- add `use_spine: true` and `spine_offset` as shown by `scan`, or provide an explicit `chapter_template` or `chapter_paths` list.

The built-in ADWD config targets the split-file epub edition. Chapter paths follow the pattern `dummy_split_###.html` with a numeric offset of 4 applied to the chapter number. Front matter files `dummy_split_001.html` and `dummy_split_003.html` contain the dedication and the "Cavil on Chronology" note respectively. A different ADWD edition can be handled the same way as described above for AFFC.
