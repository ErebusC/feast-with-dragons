package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

//go:embed configs/fwd.json
var fwdJSON []byte

//go:embed configs/boiled-leather.json
var boiledJSON []byte

//go:embed configs/ball-of-beasts.json
var ballJSON []byte

// Config is the top-level structure for a splicing JSON file.
type Config struct {
	Name        string                `json:"name"`
	Author      string                `json:"author,omitempty"`
	Series      string                `json:"series,omitempty"`
	Books       map[string]BookConfig `json:"books,omitempty"`
	FrontMatter []FrontMatterEntry    `json:"front_matter"`
	Chapters    []ChapterEntry        `json:"chapters"`
}

// BookConfig describes how to extract content from a source epub or audio
// file. All fields are optional; absent fields fall back to built-in defaults
// for recognised book IDs (AFFC, ADWD).
type BookConfig struct {
	// CSSSource is the zip-internal path for the book's stylesheet.
	CSSSource string `json:"css_src,omitempty"`
	// CSSDest is the filename written to OEBPS/Styles/ in the output.
	CSSDest string `json:"css_dest,omitempty"`
	// ImageSrcPrefix is the zip-path prefix that identifies image files
	// belonging to this book.
	ImageSrcPrefix string `json:"image_src_prefix,omitempty"`
	// ImageDest is the subdirectory name under OEBPS/Images/ in the output.
	ImageDest string `json:"image_dest,omitempty"`
	// CoverSrc is the zip-internal path for the book's cover image.
	CoverSrc string `json:"cover_src,omitempty"`
	// ChapterPaths lists zip-internal paths for each chapter in order.
	// If supplied, chapter N maps to ChapterPaths[N-1].
	ChapterPaths []string `json:"chapter_paths,omitempty"`
	// ChapterTemplate is a fmt.Sprintf template receiving the chapter number.
	// Used when ChapterPaths is absent. Example: "OEBPS/Text/chapter_%03d.html"
	ChapterTemplate string `json:"chapter_template,omitempty"`
	// StripTOCLinks removes dead anchor tags wrapping chapter title spans.
	// Required for some epub editions where the TOC links point to split files.
	StripTOCLinks bool `json:"strip_toc_links,omitempty"`
	// ExpectedChapters is used for audio sanity-check warnings only.
	ExpectedChapters int `json:"expected_chapters,omitempty"`
	// UseSpine instructs the tool to build ChapterPaths by reading the epub's
	// own OPF spine at runtime. Chapter num N maps to the Nth spine entry
	// (1-indexed). Use scan to inspect spine order before authoring a config
	// with this flag, as front matter and TOC pages are included in the count.
	// Set spine_offset to avoid counting front matter items manually.
	UseSpine bool `json:"use_spine,omitempty"`
	// SpineOffset shifts the chapter number when resolving spine entries.
	// Chapter num N maps to spine entry N+SpineOffset (1-indexed after shift).
	// Use scan to find the offset: it reports the index of the first
	// NCX-labelled spine entry. Ignored unless UseSpine or AutoDetect is set.
	SpineOffset int `json:"spine_offset,omitempty"`
	// AutoDetect instructs the tool to derive CSS, cover, image paths, and
	// the NCX chapter index from the epub's OPF manifest and NCX/nav document
	// at runtime. Fields already set explicitly in the config are left
	// unchanged. AutoDetect does NOT set UseSpine -- chapter path resolution
	// remains independent. For AFFC/ADWD and other books with built-in path
	// functions, auto_detect enriches asset detection and enables title-based
	// NCX lookup without changing how num values are mapped to files. For
	// unknown epub editions with no built-in path function, also set
	// use_spine: true to resolve chapter paths from the spine. The one field
	// that cannot be auto-detected is StripTOCLinks, which must be set
	// manually when the edition requires it.
	AutoDetect bool `json:"auto_detect,omitempty"`
	// NCXIndex maps normalizeTitle(label) -> zip path for all entries in the
	// source epub's NCX or EPUB3 nav document. Populated at runtime by
	// autoDetectBookConfig; never read from or written to JSON.
	NCXIndex map[string]string `json:"-"`
}

// FrontMatterEntry describes a single front-matter page included before the
// table of contents. File is the zip-internal path inside the source epub.
type FrontMatterEntry struct {
	Book  string `json:"book"`
	File  string `json:"file"`
	Title string `json:"title"`
}

// ChapterEntry is either a single chapter or a combined chapter.
// If Parts is non-empty the entry is built by concatenating the body content
// of each part.
type ChapterEntry struct {
	Title string        `json:"title"`
	Book  string        `json:"book,omitempty"`
	Num   int           `json:"num,omitempty"`
	Parts []ChapterPart `json:"parts,omitempty"`
}

// ChapterPart is one source chapter within a combined entry.
type ChapterPart struct {
	Book string `json:"book"`
	Num  int    `json:"num"`
}

func (e ChapterEntry) IsCombined() bool { return len(e.Parts) > 0 }

// defaultBooks returns built-in BookConfig values for the AFFC and ADWD
// epub editions. Used when a config does not supply a "books" section.
func defaultBooks() map[string]BookConfig {
	return map[string]BookConfig{
		"AFFC": {
			CSSSource:        "OEBPS/Styles/Mart_9780553900323_epub_css_r1.css",
			CSSDest:          "affc.css",
			ImageSrcPrefix:   "OEBPS/Images/",
			ImageDest:        "affc",
			CoverSrc:         "OEBPS/Images/Mart_9780553900323_epub_cvi_r1.jpg",
			ExpectedChapters: 46,
		},
		"ADWD": {
			CSSSource:        "stylesheet.css",
			CSSDest:          "adwd.css",
			ImageSrcPrefix:   "images/",
			ImageDest:        "adwd",
			CoverSrc:         "cover.jpeg",
			StripTOCLinks:    true,
			ExpectedChapters: 73,
		},
	}
}

// effectiveBooks returns a BookConfig map for every book ID referenced in the
// config, merging explicit entries over the built-in defaults.
func (cfg *Config) effectiveBooks() map[string]BookConfig {
	base := defaultBooks()
	for id, bk := range cfg.Books {
		base[id] = bk
	}
	return base
}

// loadConfig returns the Config for a named built-in splicing or reads a
// JSON file at the given path.
func loadConfig(name string) (*Config, error) {
	var data []byte
	switch name {
	case "fwd", "feast-with-dragons":
		data = fwdJSON
	case "boiled", "boiled-leather":
		data = boiledJSON
	case "ball", "ball-of-beasts":
		data = ballJSON
	default:
		var err error
		data, err = os.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("unknown splicing %q -- expected fwd, boiled, ball, or a path to a JSON config file", name)
		}
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Warn about book IDs that are not in the explicit books map and have no
	// built-in defaults. These will need to be provided via -book flags at
	// runtime. This catches typos like "AFC" instead of "AFFC" early.
	knownIDs := map[string]bool{"AFFC": true, "ADWD": true}
	for id := range cfg.Books {
		knownIDs[id] = true
	}
	warned := map[string]bool{}
	for _, ch := range cfg.Chapters {
		ids := []string{ch.Book}
		if ch.IsCombined() {
			ids = nil
			for _, p := range ch.Parts {
				ids = append(ids, p.Book)
			}
		}
		for _, id := range ids {
			if id != "" && !knownIDs[id] && !warned[id] {
				fmt.Fprintf(os.Stderr, "Note: config references book ID %q which has no built-in defaults and no entry in the \"books\" section. Supply it via -book %s=<path> at runtime.\n", id, id)
				warned[id] = true
			}
		}
	}

	return &cfg, nil
}

// chapterSourcePath returns the zip-internal path for chapter n of bookID,
// consulting the BookConfig before falling back to built-in edition logic.
// SpineOffset is applied when ChapterPaths is in use: the effective index is
// n + SpineOffset - 1 (0-based). This allows narrative chapter numbers to
// be used directly in the config without counting front matter items.
func chapterSourcePath(bookID string, num int, books map[string]BookConfig) string {
	if bk, ok := books[bookID]; ok {
		effective := num + bk.SpineOffset - 1
		if len(bk.ChapterPaths) > 0 {
			if effective >= 0 && effective < len(bk.ChapterPaths) {
				return bk.ChapterPaths[effective]
			}
			// Out of range -- return a descriptive placeholder so the
			// validation step can produce a useful error message.
			return fmt.Sprintf("OUT_OF_RANGE:spine[%d](len=%d)", effective, len(bk.ChapterPaths))
		}
		if bk.ChapterTemplate != "" {
			return fmt.Sprintf(bk.ChapterTemplate, num)
		}
	}
	switch bookID {
	case "AFFC":
		return affcChapterPath(num)
	case "ADWD":
		return adwdChapterPath(num)
	}
	return fmt.Sprintf("chapter_%03d.html", num)
}

// affcChapterPath returns the zip path for AFFC chapter n using the specific
// naming convention of the Bantam epub edition.
func affcChapterPath(n int) string {
	if n == 1 {
		return "OEBPS/Text/Mart_9780553900323_epub_prl_r1.htm"
	}
	return fmt.Sprintf("OEBPS/Text/Mart_9780553900323_epub_c%02d_r1.htm", n-1)
}

// adwdChapterPath returns the zip path for ADWD chapter n. The split-file
// edition offsets chapter numbers by 4 from the dummy_split filename index.
func adwdChapterPath(n int) string {
	return fmt.Sprintf("dummy_split_%03d.html", n+4)
}
