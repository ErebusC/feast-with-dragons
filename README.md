# feast-with-dragons

A tool for building combined epub and audiobook files from A Feast for Crows and A Dance with Dragons, interleaved in a custom reading order. Three built-in reading orders are provided, and the tool supports fully custom orders via JSON config files. Both ebook and audio output are supported.

This tool was developed with AI assistance (Claude).

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

If Go reports a `buildvcs` error, disable VCS stamping:

```
go build -buildvcs=false -o feast-with-dragons .
```

The binary embeds all six built-in configs at compile time. No additional files are required at runtime.

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
| `scan` | Print the spine of an epub, generate a skeleton config, or output JSON |
| `scan-audio` | Print the chapter list of an audio file or directory with timings |
| `validate` | Dry-run a config against source epubs without producing output |
| `validate-audio` | Dry-run an audio config: probe sources, validate mapping, and show encoding plan |
| `diff` | Compare two splicings and show chapter differences |
| `list` | List all built-in splicings with chapter counts |
| `show` | Print the full chapter-by-chapter breakdown of a splicing |

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
| `-words-per-page` | `500` | Approximate words between page markers. Overrides the `words_per_page` field in the config JSON if set. See Page numbers below |
| `-quiet` | off | Suppress progress output |
| `-force` | off | Overwrite an existing output file |

If `-affc` or `-adwd` are not provided, the tool searches the current directory for epub files whose names contain both `feast` and `crows`, or both `dance` and `dragons`, respectively. Both keywords must be present to match, so an output file named `A Feast with Dragons.epub` will not be falsely detected as the AFFC source.

If the output file already exists the tool refuses to proceed unless `-force` is set.

### Page numbers

The output epub includes page markers inserted at regular word-count intervals throughout each chapter. These markers are referenced by a `<pageList>` in the NCX, which tells readers like Kindle to display page numbers instead of raw locations.

The default interval of 500 words per page produces roughly one page advance per screen tap on Kindle at typical font sizes. If pages advance too quickly (multiple pages per tap), increase the value. If they advance too slowly (tapping several times before the page number changes), decrease it.

```
feast-with-dragons ebook -words-per-page 300    # finer, ~2400 pages
feast-with-dragons ebook -words-per-page 500    # default, ~1500 pages
feast-with-dragons ebook -words-per-page 1000   # coarser, ~750 pages
```

The right value depends on your font size and screen size. The page count is reported during the build.

The `words_per_page` field can also be set in the config JSON. The resolution order is: `-words-per-page` flag > `words_per_page` in config > built-in default of 500.

### Cover image

The cover page displays the cover image only, with no text overlay. This ensures compatibility with Kindle and other readers that do not support CSS absolute positioning in epub content. Title and author information is stored in the OPF metadata and displayed by readers in their library view.

The cover image is resolved in this order:

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
feast-with-dragons ebook -words-per-page 300 -numbered-toc
```

---

## audio

Builds a spliced M4B by extracting chapter segments from source audio files and concatenating them in the order defined by the active splicing config. Chapter metadata and optional cover art are embedded in the output file.

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
| `-cover` | auto-detected | Cover image to embed in the output file (JPEG or PNG) |
| `-j` | number of CPUs | Parallel extraction workers. Use `-j 1` for sequential extraction |
| `-dry-run` | off | Print every ffmpeg command that would be run without executing anything |
| `-quiet` | off | Suppress progress output |
| `-force` | off | Overwrite an existing output file and skip the re-encode confirmation prompt |

If `-affc` or `-adwd` are not provided, the tool searches the current directory for audio files whose names contain both `feast` and `crows`, or both `dance` and `dragons`, respectively. Both keywords must be present to match. Multi-part audiobooks are supported: all matching files in the directory are collected and probed in filename order.

### Chapter matching

Chapter assignment is positional. The Nth chapter in the config maps to the Nth chapter segment found in the audio source after filtering. The title from the config is compared against the audio metadata title as a sanity check only; a mismatch produces a NOTE in the output but does not stop the build. This is expected for AFFC, which labels chapters numerically in some editions.

Chapters are filtered before assignment. Segments shorter than 30 seconds are dropped. Segments whose lowercase title is `intro` or `credits` are also dropped.

Consecutive segments with the same title, including segments that span multiple audio files, are merged into a single logical chapter before assignment. This handles editions that split a chapter across tracks or across parts of a multi-part audiobook.

### Build process

The audio build has three phases:

1. **Probe**: each source audio file is scanned for embedded chapter metadata. The first and last few chapter titles are printed so you can verify the segmentation looks correct. The chapter count is compared against the expected count in the config. The audio stream format (codec, sample rate, channels, bitrate) is probed from each source book to determine the target encoding parameters.

2. **Extract**: each chapter segment is extracted to a temporary work directory beside the output file. Segments whose source book format already matches the target (same AAC codec, sample rate, and channel count) are stream-copied without re-encoding — this is essentially instant. Segments from books with a different format are re-encoded to match the target. The target format is derived from the highest-quality source book (highest sample rate and channel count). This hybrid approach minimises re-encoding time: in a typical AFFC/ADWD build, ADWD (44100 Hz) segments are stream-copied and only AFFC (22050 Hz) segments are re-encoded.

   Segments with `audio_start` or `audio_end` overrides are always re-encoded regardless of format, because stream copy only cuts on keyframe boundaries and cannot make sample-accurate cuts at arbitrary timestamps.

   When re-encoding is required, the tool prints the encoding plan and prompts for confirmation before starting. Pass `-force` to suppress the prompt for non-interactive use.

   Extractions run in parallel using the number of workers set by `-j`. Progress is printed every 10 segments with elapsed time.

3. **Concatenate**: the extracted segments are concatenated into the output file with chapter metadata and a faststart pass that moves the MP4 moov atom to the front of the file for faster seeking.

### Resume support

The work directory uses a deterministic name (`.feast-audio-<outputname>` beside the output file) rather than a random temp directory. If the build is interrupted, re-running the same command will skip any segment files already present and larger than 1 KB, resuming from where it left off. The work directory is removed automatically on successful completion. On failure it is kept, and its path is printed so you can inspect it.

### Cover art

The tool auto-detects a cover image by searching the directories containing the source audio files for any of these filenames: `cover.jpg`, `cover.jpeg`, `cover.png`, `folder.jpg`, `folder.jpeg`, `folder.png`. The first match is used. The `-cover` flag overrides this with an explicit path. If no cover is found or specified, the output file has no embedded artwork.

### Probe caching

ffprobe results (chapter lists and audio formats) are cached per source file in `~/.cache/feast-with-dragons/`. Repeated `validate-audio` or `audio` runs on the same source files skip the probe entirely and use the cached data. The cache is keyed by a hash of the absolute file path and is automatically invalidated when the file's modification time or size changes.

### Diagnostic output

Before extraction begins, the tool prints a full mapping of config chapters to audio segments, showing the source book, segment number, audio title, time position, and whether each book will be stream-copied or re-encoded:

```
Validating chapter mapping...
  [001] Prologue (Pate) -> AFFC seg 1 "Chapter 1" (0.0s-2612.8s)
  [002] Prologue (Varamyr) -> ADWD seg 1 "Prologue" (15.3s-2434.6s)
  ...
Format:   ADWD aac 44100Hz 2ch — stream copy
Format:   AFFC aac 22050Hz 2ch — would re-encode to 44100Hz 2ch
```

Use `validate-audio` to see this output without starting the build.

### Examples

```
feast-with-dragons audio
feast-with-dragons audio -affc ./affc.m4b -adwd ./adwd-part1.m4b
feast-with-dragons audio -splicing boiled -out "Boiled Leather.m4b"
feast-with-dragons audio -j 2
feast-with-dragons audio -dry-run
feast-with-dragons audio -cover ./my-cover.jpg
feast-with-dragons audio -force   # skips re-encode prompt, overwrites existing output
```

---

## merge

Concatenates two or more epub or audio files into a single output file without chapter-level splicing. For audio, chapter markers from each source file are preserved and their timestamps adjusted to be contiguous in the output. Audio output includes a faststart pass for faster seeking.

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

## scan-audio

Probes an audio file or directory and prints the embedded chapter list with start times, end times, and durations. Useful for inspecting source files when authoring configs or finding the correct `audio_start`/`audio_end` split points.

```
feast-with-dragons scan-audio <file-or-directory>
```

When given a directory, all audio files in it are probed in filename order and their chapters are listed with a running chapter number across all files.

### Examples

```
feast-with-dragons scan-audio ./affc.m4b
feast-with-dragons scan-audio ./adwd-parts/
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

## validate-audio

Probes source audio files and validates the chapter-to-segment mapping without extracting any audio. Also prints the encoding plan (which books will be stream-copied and which will be re-encoded) so you can review it before starting the build.

```
feast-with-dragons validate-audio [flags]
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-splicing` | `fwd` | Splicing to validate |
| `-affc` | auto-detected | File or directory for AFFC audio |
| `-adwd` | auto-detected | File or directory for ADWD audio |
| `-book id=path` | | Audio file or directory for a custom book ID; repeatable |
| `-quiet` | off | Suppress per-chapter mapping output |

### Examples

```
feast-with-dragons validate-audio
feast-with-dragons validate-audio -splicing fwd-audible
feast-with-dragons validate-audio -quiet   # show only the summary
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

## list

Lists all built-in splicings with their chapter counts and a short description.

```
feast-with-dragons list
```

---

## show

Prints the full chapter-by-chapter breakdown of a splicing: position, source book, chapter number within that book, and title. Combined chapters and audio overrides are annotated.

```
feast-with-dragons show [splicing]
```

If no splicing is specified, `fwd` is used.

### Examples

```
feast-with-dragons show
feast-with-dragons show boiled
feast-with-dragons show fwd-audible   # shows [audio_num] and [audio_override] annotations
```

---

## Built-in splicings

Six reading orders are embedded in the binary: three standard configs and three with audio overrides for the Audible AFFC edition.

### fwd -- A Feast with Dragons

The most widely recommended interleaved reading order. The two prologues are read first, then chapters from both books are interleaved chronologically. Samwell I and Jon II appear as separate chapters. The Melisandre chapter is titled "Melisandre I" and the Victarion chapter near the end is titled "Victarion I". Produces 119 chapters.

Selected with `-splicing fwd` or `-splicing feast-with-dragons`.

### boiled -- Boiled Leather

A reading order based on the Boiled Leather blog's recommendation. Broadly similar to fwd but with some reordering of chapters in the middle section. Samwell I and Jon II appear as separate chapters. Produces 119 chapters.

Selected with `-splicing boiled` or `-splicing boiled-leather`.

### ball -- A Ball of Beasts

A variation that combines Samwell I and Jon II into a single chapter. The Melisandre chapter is titled "The Red Priestess" and the Victarion chapter near the end is titled "The One of Two Gods". Produces 118 chapters.

Selected with `-splicing ball` or `-splicing ball-of-beasts`.

### Audible AFFC edition variants

The Audible unabridged AFFC audiobook has a chapter mapping issue: Cersei IX and The Princess in the Tower are recorded as a single audio track (Chapter 40 in the audiobook metadata), which shifts every subsequent AFFC chapter position by one. The final chapter marker (Chapter 46) is a 38-second credits segment, not Samwell V.

The `-audible` variants of each splicing include audio-specific overrides that correct for this:

- Cersei IX is extracted from the first part of audio segment 40
- The Princess in the Tower is extracted from the second part of audio segment 40, starting at the chapter transition (approximately 37m 13s into the segment)
- Chapters 42-46 (Alayne II through Samwell V) are shifted back by one audio segment

These overrides only affect the audio build. The epub output is identical to the standard configs.

Selected with `-splicing fwd-audible`, `-splicing boiled-audible`, or `-splicing ball-audible`.

If the Cersei IX / Princess in the Tower transition sounds clipped or has a few seconds of overlap in your output, adjust the `audio_start` and `audio_end` values in the config JSON. The split point is approximate and may need fine-tuning for your specific file. See Audio chapter overrides below.

---

## Custom configs

Any JSON file can be passed to `-splicing`. The schema is as follows.

### Top-level fields

| Field | Required | Description |
|---|---|---|
| `name` | yes | Title of the output book, used for the epub title and default output filename |
| `author` | no | Author name written into epub and audio metadata. Defaults to `George R. R. Martin` for epub and `Unknown` for audio if omitted |
| `series` | no | Series name stored in metadata |
| `words_per_page` | no | Default words-per-page interval for ebook page markers. Overridden by the `-words-per-page` flag |
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
| `audio_num` | no | Overrides `num` for audio segment lookup. Use when the audiobook has fewer chapter markers than the epub |
| `audio_start` | no | Absolute timestamp (seconds) in the source audio file. Extraction begins here instead of the segment's metadata start time |
| `audio_end` | no | Absolute timestamp (seconds) in the source audio file. Extraction ends here instead of the segment's metadata end time |

The `audio_num`, `audio_start`, and `audio_end` fields are ignored by the epub builder. They exist to handle audiobook editions where two book chapters are merged into one audio track, or where chapter markers don't align with the epub's chapter numbering. See Audio chapter overrides below.

A combined chapter (epub only) concatenates the body content of two or more source chapters into one output file, with a horizontal rule between parts:

| Field | Required | Description |
|---|---|---|
| `title` | yes | Chapter title in the output table of contents |
| `parts` | yes | Array of `{"book": "<id>", "num": <n>}` objects |

### Audio chapter overrides

When an audiobook edition has fewer chapter markers than the epub, positional mapping breaks. For example, if the audiobook merges chapters 40 and 41 into a single track, then audio segment 41 contains the content of epub chapter 42, segment 42 contains chapter 43, and so on.

Three chapter-level fields correct for this without affecting the epub build:

`audio_num` tells the audio builder to look up a different segment number than `num`. In the example above, epub chapters 42-46 would each set `audio_num` to one less than their `num` value.

`audio_start` and `audio_end` allow a single audio segment to be split into two chapter entries. One entry sets `audio_end` at the transition point, and the next entry sets `audio_num` to the same segment with `audio_start` at that point. This extracts two separate output chapters from one source track.

Note: segments with `audio_start` or `audio_end` set are always re-encoded regardless of source format, because stream copy cannot make sample-accurate cuts at arbitrary timestamps.

Example: AFFC Cersei IX and The Princess in the Tower are merged in audio segment 40, with the transition at 104318.8 seconds:

```json
{"title": "Cersei IX",                "book": "AFFC", "num": 40, "audio_end": 104318.8},
{"title": "The Princess in the Tower", "book": "AFFC", "num": 41, "audio_num": 40, "audio_start": 104318.8},
{"title": "Alayne II",                "book": "AFFC", "num": 42, "audio_num": 41},
{"title": "Brienne VIII",             "book": "AFFC", "num": 43, "audio_num": 42}
```

The `num` values remain correct for the epub builder. The `audio_num` and timestamp overrides only affect the audio build. The built-in `-audible` configs use this pattern for the Audible unabridged AFFC edition.

### Minimal custom config example

```json
{
  "name": "My Reading Order",
  "author": "George R. R. Martin",
  "words_per_page": 400,
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

This tool does not include or distribute any copyrighted content. Users must supply their own legally obtained source files.
