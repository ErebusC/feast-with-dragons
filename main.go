// feast-with-dragons merges epub and audio files into a combined reading order defined
// by a JSON splicing config. Audio splicing is also supported via ffmpeg.
//
// Usage:
//
//	feast-with-dragons ebook [flags]
//	feast-with-dragons audio [flags]
//	feast-with-dragons merge [flags] file1 file2 ...
//	feast-with-dragons scan  <epub>
package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Package-level flags set by each subcommand.
var (
	quietMode bool
	forceMode bool
)

// logf prints a message to stdout unless quiet mode is active.
func logf(format string, args ...any) {
	if !quietMode {
		fmt.Printf(format, args...)
	}
}

// ---------------------------------------------------------------------------
// Zip helpers
// ---------------------------------------------------------------------------

func zipIndex(r *zip.Reader) map[string]*zip.File {
	m := make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		m[f.Name] = f
	}
	return m
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func writeZipEntry(w *zip.Writer, name string, data []byte) error {
	fw, err := w.Create(name)
	if err != nil {
		return err
	}
	_, err = fw.Write(data)
	return err
}

// checkedZipWriter wraps a *zip.Writer and records the first error
// encountered. Subsequent writes become no-ops after the first failure.
// This avoids littering builder functions with per-call error checks
// while still surfacing the underlying problem.
type checkedZipWriter struct {
	w   *zip.Writer
	err error
}

func (cw *checkedZipWriter) write(name string, data []byte) {
	if cw.err != nil {
		return
	}
	cw.err = writeZipEntry(cw.w, name, data)
}

// ---------------------------------------------------------------------------
// Chapter path resolution
// ---------------------------------------------------------------------------

// resolveChapterPath returns the zip-internal path for a chapter, preferring
// NCX title-based lookup over num-based lookup when possible.
//
// If the book has an NCXIndex (populated by autoDetectBookConfig), two lookups
// are attempted in order:
//
//  1. Exact lowercase match: works for editions where the NCX includes full
//     labels like "JON I", "TYRION II", allowing precise per-chapter matching.
//
//  2. Parenthetical-stripped lowercase match: "Prologue (Pate)" -> "prologue",
//     matching NCX entries like "PROLOGUE". Only the parenthetical is stripped;
//     roman numerals are deliberately preserved to avoid false matches -- if
//     "Jon I" and "Jon II" both normalised to "jon", they would both resolve to
//     the same (first) NCX entry.
//
// If neither lookup succeeds, or if title is empty, the call falls back to
// chapterSourcePath, which uses SpineOffset, ChapterPaths, ChapterTemplate, or
// the built-in edition path logic.
func resolveChapterPath(bookID string, num int, title string, books map[string]BookConfig) string {
	if bk, ok := books[bookID]; ok && title != "" && len(bk.NCXIndex) > 0 {
		lower := strings.ToLower(strings.TrimSpace(title))

		// 1. Exact lowercase match.
		if p, found := bk.NCXIndex[lower]; found {
			return p
		}

		// 2. Parenthetical-stripped match only (no numeral stripping).
		if i := strings.Index(lower, "("); i >= 0 {
			stripped := strings.TrimSpace(lower[:i])
			if stripped != lower {
				if p, found := bk.NCXIndex[stripped]; found {
					return p
				}
			}
		}
	}
	return chapterSourcePath(bookID, num, books)
}

// ---------------------------------------------------------------------------
// HTML rewriting
// ---------------------------------------------------------------------------

var (
	reAnyCSS       = regexp.MustCompile(`<link[^>]+rel="stylesheet"[^>]*/?>`)
	rePageTemplate = regexp.MustCompile(`\s*<link[^>]*page-template\.xpgt[^>]*/?>`)
	reImgSrc       = regexp.MustCompile(`src="[^"]+\.(jpg|jpeg|png|gif|svg)"`)
	reTOCLink      = regexp.MustCompile(`<a[^>]+href="[^"]*"[^>]*>(<span[^>]*>[^<]*</span>)</a>`)
)

// rewriteHTML updates asset paths in a source chapter to match the output
// epub structure. CSS links are replaced with the book's output stylesheet,
// image src paths are rewritten to the book's image subdirectory, and any
// unsupported link types (e.g. Adobe page templates) are stripped.
func rewriteHTML(src []byte, bookID string, bk BookConfig) []byte {
	src = rePageTemplate.ReplaceAll(src, nil)

	if bk.CSSDest != "" {
		newLink := fmt.Sprintf(`<link href="../Styles/%s" rel="stylesheet" type="text/css"/>`, bk.CSSDest)
		src = reAnyCSS.ReplaceAll(src, []byte(newLink))
	}

	if bk.ImageDest != "" {
		src = reImgSrc.ReplaceAllFunc(src, func(match []byte) []byte {
			s := string(match)
			fname := path.Base(s[5 : len(s)-1]) // strip src=" and "
			return []byte(fmt.Sprintf(`src="../Images/%s/%s"`, bk.ImageDest, fname))
		})
	}

	if bk.StripTOCLinks {
		src = reTOCLink.ReplaceAll(src, []byte(`$1`))
	}

	return src
}

// ---------------------------------------------------------------------------
// OPF / NCX generation
// ---------------------------------------------------------------------------

const containerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

func buildOPF(bookID, title, author string, manifestItems, spineItems []string) string {
	if author == "" {
		author = "Unknown"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="BookId">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>%s</dc:title>
    <dc:creator opf:role="aut">%s</dc:creator>
    <dc:language>en</dc:language>
    <dc:identifier id="BookId">%s</dc:identifier>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="cover-image" href="Images/cover.jpg" media-type="image/jpeg"/>
%s
  </manifest>
  <spine toc="ncx">
%s
  </spine>
  <guide>
    <reference type="cover" title="Cover" href="Text/cover.html"/>
    <reference type="toc" title="Table of Contents" href="Text/toc.html"/>
    <reference type="text" title="Begin Reading" href="Text/chapter_001.html"/>
  </guide>
</package>`,
		xmlEscape(title),
		xmlEscape(author),
		bookID,
		strings.Join(manifestItems, "\n"),
		strings.Join(spineItems, "\n"),
	)
}

func buildNCX(bookID, title string, navPoints []string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE ncx PUBLIC "-//NISO//DTD ncx 2005-1//EN" "http://www.daisy.org/z3986/2005/ncx-2005-1.dtd">
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head>
    <meta name="dtb:uid" content="%s"/>
    <meta name="dtb:depth" content="1"/>
    <meta name="dtb:totalPageCount" content="0"/>
    <meta name="dtb:maxPageNumber" content="0"/>
  </head>
  <docTitle><text>%s</text></docTitle>
  <navMap>
%s
  </navMap>
</ncx>`,
		bookID,
		xmlEscape(title),
		strings.Join(navPoints, "\n"),
	)
}

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

func imageMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	default:
		return "image/jpeg"
	}
}

func sanitiseID(s string) string {
	return strings.NewReplacer(".", "_", "-", "_", " ", "_").Replace(s)
}

var isImageFile = regexp.MustCompile(`(?i)\.(jpg|jpeg|png|gif|svg)$`)

// ---------------------------------------------------------------------------
// Ebook builder
// ---------------------------------------------------------------------------

// runEbook builds a spliced epub. sources maps each book ID used in cfg to
// the path of its source epub file. If annotate is true, a small source
// annotation is appended to each chapter indicating which book it came from.
func runEbook(sources map[string]string, outputPath string, cfg *Config, annotate bool) error {
	books := cfg.effectiveBooks()

	// Open all source epubs.
	zipReaders := make(map[string]*zip.ReadCloser, len(sources))
	zipIndexes := make(map[string]map[string]*zip.File, len(sources))
	for id, path := range sources {
		logf("Opening %s: %s\n", id, path)
		rc, err := zip.OpenReader(path)
		if err != nil {
			return fmt.Errorf("opening %s epub: %w", id, err)
		}
		defer rc.Close()
		zipReaders[id] = rc
		zipIndexes[id] = zipIndex(&rc.Reader)
	}

	// For any book with AutoDetect set, inspect the OPF manifest to fill in
	// asset paths (CSS, cover, images) and populate the NCX chapter index.
	// This does not affect chapter path resolution -- UseSpine and SpineOffset
	// are handled separately below. For AFFC/ADWD and other books with
	// built-in path functions, auto_detect enriches asset detection and
	// enables NCX title-based lookup without disturbing the existing num
	// mapping. For unknown editions without a built-in path function, also
	// set use_spine: true in the config to resolve chapter paths from the spine.
	for id, bk := range books {
		if !bk.AutoDetect {
			continue
		}
		rc, ok := zipReaders[id]
		if !ok {
			return fmt.Errorf("auto_detect is set for %s but no source epub was provided", id)
		}
		detected, err := autoDetectBookConfig(id, &rc.Reader, bk)
		if err != nil {
			return fmt.Errorf("auto-detecting config for %s: %w", id, err)
		}
		ncxCount := len(detected.NCXIndex)
		logf("  %s auto-detect: css=%s cover=%s image_prefix=%s ncx_entries=%d\n",
			id, detected.CSSSource, detected.CoverSrc, detected.ImageSrcPrefix, ncxCount)
		if ncxCount > 0 {
			logf("  %s: title-based chapter lookup active (NCX matched)\n", id)
		} else {
			logf("  %s: no NCX found -- chapter lookup falls back to spine num\n", id)
		}
		if !detected.StripTOCLinks {
			logf("  %s: strip_toc_links not set -- if chapter titles appear as dead links in the output, add \"strip_toc_links\": true to this book's config\n", id)
		}
		books[id] = detected
	}

	// For any book with UseSpine set, read the epub's own OPF spine and
	// populate ChapterPaths so the rest of the build treats it identically
	// to an explicit path list.
	for id, bk := range books {
		if !bk.UseSpine {
			continue
		}
		rc, ok := zipReaders[id]
		if !ok {
			return fmt.Errorf("use_spine is set for %s but no source epub was provided", id)
		}
		paths, err := resolveSpinePaths(&rc.Reader)
		if err != nil {
			return fmt.Errorf("resolving spine for %s: %w", id, err)
		}
		logf("  %s: %d spine items resolved from OPF\n", id, len(paths))
		if bk.SpineOffset > 0 {
			logf("  %s: spine_offset=%d applied (narrative ch N -> spine entry N+%d)\n",
				id, bk.SpineOffset, bk.SpineOffset)
		}
		bk.ChapterPaths = paths
		books[id] = bk
	}

	// Validate all chapters are present before writing anything.
	logf("Validating source chapters...\n")
	var missing []string
	check := func(bookID string, num int, title string) {
		p := resolveChapterPath(bookID, num, title, books)
		if strings.HasPrefix(p, "OUT_OF_RANGE:") {
			missing = append(missing, fmt.Sprintf("  %q (%s ch%d) -> %s -- num exceeds spine length; check spine_offset or use auto_detect", title, bookID, num, p))
			return
		}
		if strings.HasPrefix(p, "NON_XHTML:") {
			missing = append(missing, fmt.Sprintf("  %q (%s ch%d) -> %s -- spine entry is not XHTML (image, SVG, or other non-text item)", title, bookID, num, p))
			return
		}
		if _, ok := zipIndexes[bookID][p]; !ok {
			missing = append(missing, fmt.Sprintf("  %q (%s ch%d) -> %s", title, bookID, num, p))
		}
	}
	for _, entry := range cfg.Chapters {
		if entry.IsCombined() {
			for _, part := range entry.Parts {
				// Parts have no individual title; force num-based lookup.
				check(part.Book, part.Num, "")
			}
		} else {
			check(entry.Book, entry.Num, entry.Title)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing source files:\n%s", strings.Join(missing, "\n"))
	}
	logf("  All %d chapters found.\n\n", len(cfg.Chapters))

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output file: %w", err)
	}
	defer outFile.Close()

	out := zip.NewWriter(outFile)
	defer out.Close()

	cw := &checkedZipWriter{w: out}

	// mimetype must be the first entry and stored uncompressed.
	mh := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	mw, err := out.CreateHeader(mh)
	if err != nil {
		return fmt.Errorf("writing mimetype: %w", err)
	}
	if _, err := mw.Write([]byte("application/epub+zip")); err != nil {
		return fmt.Errorf("writing mimetype: %w", err)
	}

	cw.write("META-INF/container.xml", []byte(containerXML))

	// Stylesheets and images -- one pass per source book.
	var manifestItems []string

	// Track which book IDs are actually referenced in the config.
	usedBooks := map[string]bool{}
	for _, ch := range cfg.Chapters {
		if ch.IsCombined() {
			for _, p := range ch.Parts {
				usedBooks[p.Book] = true
			}
		} else {
			usedBooks[ch.Book] = true
		}
	}
	for _, fm := range cfg.FrontMatter {
		usedBooks[fm.Book] = true
	}

	// Sort for deterministic output.
	var usedBookIDs []string
	for id := range usedBooks {
		usedBookIDs = append(usedBookIDs, id)
	}
	sort.Strings(usedBookIDs)

	for _, id := range usedBookIDs {
		bk := books[id]
		idx := zipIndexes[id]

		// CSS
		if bk.CSSSource != "" && bk.CSSDest != "" {
			if f, ok := idx[bk.CSSSource]; ok {
				data, err := readZipEntry(f)
				if err != nil {
					return fmt.Errorf("reading %s CSS: %w", id, err)
				}
				cw.write("OEBPS/Styles/"+bk.CSSDest, data)
				manifestItems = append(manifestItems,
					fmt.Sprintf(`    <item id="%s_css" href="Styles/%s" media-type="text/css"/>`,
						strings.ToLower(id), bk.CSSDest))
			}
		}

		// Images
		if bk.ImageSrcPrefix != "" && bk.ImageDest != "" {
			for name, f := range idx {
				if !isImageFile.MatchString(name) {
					continue
				}
				if bk.ImageSrcPrefix != "" && !strings.HasPrefix(name, bk.ImageSrcPrefix) {
					continue
				}
				if strings.HasPrefix(path.Base(name), "cover") {
					continue
				}
				data, err := readZipEntry(f)
				if err != nil {
					return fmt.Errorf("reading %s image %s: %w", id, name, err)
				}
				base := path.Base(name)
				destPath := "OEBPS/Images/" + bk.ImageDest + "/" + base
				cw.write(destPath, data)
				manifestItems = append(manifestItems,
					fmt.Sprintf(`    <item id="%s_img_%s" href="Images/%s/%s" media-type="%s"/>`,
						strings.ToLower(id), sanitiseID(base), bk.ImageDest, base, imageMIME(base)))
			}
		}
	}

	// Cover image: user-supplied > generated blend > first available book cover.
	var coverJPEG []byte
	if userCover, err := os.ReadFile("cover.jpg"); err == nil {
		logf("Using user-supplied cover.jpg\n")
		coverJPEG = userCover
	} else {
		// Try to generate a blended cover from the first two books with covers.
		var coverData [][]byte
		for _, id := range usedBookIDs {
			bk := books[id]
			if bk.CoverSrc == "" {
				continue
			}
			idx := zipIndexes[id]
			if f, ok := idx[bk.CoverSrc]; ok {
				data, err := readZipEntry(f)
				if err == nil {
					coverData = append(coverData, data)
				}
			}
			if len(coverData) == 2 {
				break
			}
		}
		if len(coverData) >= 2 {
			logf("Generating merged cover...\n")
			coverJPEG, err = generateCover(coverData[0], coverData[1])
			if err != nil {
				return fmt.Errorf("generating cover: %w", err)
			}
			logf("  Cover generated.\n")
		} else if len(coverData) == 1 {
			logf("Using single book cover.\n")
			coverJPEG = coverData[0]
		}
	}
	if coverJPEG != nil {
		cw.write("OEBPS/Images/cover.jpg", coverJPEG)
	}

	// Cover HTML page.
	series := cfg.Series
	if series == "" {
		series = "A Song of Ice and Fire"
	}
	author := cfg.Author
	if author == "" {
		author = "George R. R. Martin"
	}
	coverHTML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd">
<html xmlns="http://www.w3.org/1999/xhtml">
<head>
  <title>%s</title>
  <style type="text/css">
    body  { margin: 0; padding: 0; background: #000; }
    .wrap { position: relative; display: inline-block; width: 100%%; }
    img   { display: block; width: 100%%; height: auto; }
    .text { position: absolute; top: 0; left: 0; right: 0;
            text-align: center; padding-top: 4%%; }
    .title  { font-family: serif; font-size: 5vw; color: #dcc89b;
               letter-spacing: 0.08em; text-transform: uppercase;
               text-shadow: 2px 2px 6px #000; display: block; }
    .series { font-family: serif; font-size: 2.4vw; color: #ece8dc;
               text-shadow: 1px 1px 4px #000; display: block; margin-top: 0.4em; }
    .author { position: absolute; bottom: 0; left: 0; right: 0;
               text-align: center; padding-bottom: 3%%;
               font-family: serif; font-size: 2.4vw; color: #ece8dc;
               text-shadow: 1px 1px 4px #000; }
  </style>
</head>
<body>
  <div class="wrap">
    <img src="../Images/cover.jpg" alt="%s"/>
    <div class="text">
      <span class="title">%s</span>
      <span class="series">%s</span>
    </div>
    <div class="author">%s</div>
  </div>
</body>
</html>`,
		xmlEscape(cfg.Name),
		xmlEscape(cfg.Name),
		xmlEscape(cfg.Name),
		xmlEscape(series),
		xmlEscape(author),
	)
	cw.write("OEBPS/Text/cover.html", []byte(coverHTML))

	// Build spine, manifest, and NCX nav.
	var spineItems, navPoints []string
	playOrder := 1

	// Cover in spine but not NCX.
	manifestItems = append(manifestItems,
		`    <item id="cover" href="Text/cover.html" media-type="application/xhtml+xml"/>`)
	spineItems = append(spineItems, `    <itemref idref="cover"/>`)

	// Front matter.
	for i, fm := range cfg.FrontMatter {
		idx := zipIndexes[fm.Book]
		bk := books[fm.Book]
		raw, err := readZipEntry(idx[fm.File])
		if err != nil {
			return fmt.Errorf("reading front matter %s: %w", fm.File, err)
		}
		html := rewriteHTML(raw, fm.Book, bk)
		banner := fmt.Sprintf(
			`<p style="font-size:0.75em;text-align:center;color:#666;margin-bottom:1.5em;">[From %s]</p>`,
			xmlEscape(fm.Book))
		html = appendAfterBodyTag(html, []byte(banner))
		label := fmt.Sprintf("fm_%02d", i)
		cw.write("OEBPS/Text/"+label+".html", html)
		manifestItems = append(manifestItems,
			fmt.Sprintf(`    <item id="%s" href="Text/%s.html" media-type="application/xhtml+xml"/>`, label, label))
		spineItems = append(spineItems, fmt.Sprintf(`    <itemref idref="%s"/>`, label))
		navPoints = append(navPoints, fmt.Sprintf(
			"    <navPoint id=\"np-%d\" playOrder=\"%d\">\n      <navLabel><text>%s</text></navLabel>\n      <content src=\"Text/%s.html\"/>\n    </navPoint>",
			playOrder, playOrder, xmlEscape(fm.Title), label))
		playOrder++
	}

	// HTML table of contents.
	var tocRows strings.Builder
	for i, entry := range cfg.Chapters {
		tocRows.WriteString(fmt.Sprintf(
			"      <li><a href=\"chapter_%03d.html\">%s</a></li>\n",
			i+1, xmlEscape(entry.Title)))
	}
	tocHTML := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd">
<html xmlns="http://www.w3.org/1999/xhtml">
<head>
  <title>Table of Contents</title>
  <style type="text/css">
/*<![CDATA[*/
    body { font-family: serif; margin: 2em; }
    h1   { text-align: center; margin-bottom: 1.5em; }
    ol   { list-style: none; padding: 0; }
    li   { margin: 0.3em 0; }
    a    { text-decoration: none; color: inherit; }
    a:hover { text-decoration: underline; }
/*]]>*/
  </style>
</head>
<body>
  <h1>%s</h1>
  <ol>
%s  </ol>
</body>
</html>`, xmlEscape(cfg.Name), tocRows.String())
	cw.write("OEBPS/Text/toc.html", []byte(tocHTML))
	manifestItems = append(manifestItems,
		`    <item id="toc-page" href="Text/toc.html" media-type="application/xhtml+xml"/>`)
	spineItems = append(spineItems, `    <itemref idref="toc-page"/>`)
	navPoints = append(navPoints, fmt.Sprintf(
		"    <navPoint id=\"np-%d\" playOrder=\"%d\">\n      <navLabel><text>Table of Contents</text></navLabel>\n      <content src=\"Text/toc.html\"/>\n    </navPoint>",
		playOrder, playOrder))
	playOrder++

	// Chapters in splicing order.
	logf("Writing chapters...\n")
	for i, entry := range cfg.Chapters {
		pos := i + 1
		destName := fmt.Sprintf("chapter_%03d.html", pos)

		var chHTML []byte
		if entry.IsCombined() {
			var err error
			chHTML, err = buildCombinedChapter(entry, zipIndexes, books)
			if err != nil {
				return fmt.Errorf("building combined chapter %q: %w", entry.Title, err)
			}
			logf("  [%03d/%d] %s (combined)\n", pos, len(cfg.Chapters), entry.Title)
		} else {
			src := resolveChapterPath(entry.Book, entry.Num, entry.Title, books)
			raw, err := readZipEntry(zipIndexes[entry.Book][src])
			if err != nil {
				return fmt.Errorf("reading chapter %s: %w", src, err)
			}
			chHTML = rewriteHTML(raw, entry.Book, books[entry.Book])
			logf("  [%03d/%d] %s (%s ch%d)\n", pos, len(cfg.Chapters), entry.Title, entry.Book, entry.Num)
		}

		if annotate {
			// Determine the source book(s) for the annotation.
			var srcLabel string
			if entry.IsCombined() {
				ids := make([]string, len(entry.Parts))
				for j, p := range entry.Parts {
					ids[j] = p.Book
				}
				srcLabel = strings.Join(ids, " + ")
			} else {
				srcLabel = entry.Book
			}
			banner := fmt.Sprintf(
				`<p style="font-size:0.65em;text-align:center;color:#999;margin-top:2em;">[%s]</p>`,
				xmlEscape(srcLabel))
			// Insert before </body>.
			if idx := bytes.LastIndex(chHTML, []byte("</body>")); idx >= 0 {
				chHTML = append(chHTML[:idx], append([]byte(banner+"\n"), chHTML[idx:]...)...)
			}
		}

		cw.write("OEBPS/Text/"+destName, chHTML)

		id := fmt.Sprintf("chapter_%03d", pos)
		manifestItems = append(manifestItems,
			fmt.Sprintf(`    <item id="%s" href="Text/%s" media-type="application/xhtml+xml"/>`, id, destName))
		spineItems = append(spineItems, fmt.Sprintf(`    <itemref idref="%s"/>`, id))
		navPoints = append(navPoints, fmt.Sprintf(
			"    <navPoint id=\"np-%d\" playOrder=\"%d\">\n      <navLabel><text>%s</text></navLabel>\n      <content src=\"Text/%s\"/>\n    </navPoint>",
			playOrder, playOrder, xmlEscape(entry.Title), destName))
		playOrder++
	}

	bookID := sanitiseID(strings.ToLower(cfg.Name))
	cw.write("OEBPS/content.opf",
		[]byte(buildOPF(bookID, cfg.Name, author, manifestItems, spineItems)))
	cw.write("OEBPS/toc.ncx",
		[]byte(buildNCX(bookID, cfg.Name, navPoints)))

	if cw.err != nil {
		return fmt.Errorf("writing epub contents: %w", cw.err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("finalising epub: %w", err)
	}

	info, _ := os.Stat(outputPath)
	logf("\nDone. Output: %s (%.1f MB)\n", outputPath, float64(info.Size())/1_048_576)
	return nil
}

// buildCombinedChapter concatenates body content from multiple source chapters
// into one HTML file, linking all relevant stylesheets.
func buildCombinedChapter(entry ChapterEntry, zipIndexes map[string]map[string]*zip.File, books map[string]BookConfig) ([]byte, error) {
	var bodies [][]byte
	var cssDests []string
	seen := map[string]bool{}
	for _, part := range entry.Parts {
		src := resolveChapterPath(part.Book, part.Num, "", books)
		raw, err := readZipEntry(zipIndexes[part.Book][src])
		if err != nil {
			return nil, fmt.Errorf("reading part %s ch%d: %w", part.Book, part.Num, err)
		}
		bk := books[part.Book]
		html := rewriteHTML(raw, part.Book, bk)
		bodies = append(bodies, extractBodyContent(html))
		if bk.CSSDest != "" && !seen[bk.CSSDest] {
			cssDests = append(cssDests, bk.CSSDest)
			seen[bk.CSSDest] = true
		}
	}

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	sb.WriteString(`<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd">` + "\n")
	sb.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml">` + "\n<head>\n")
	sb.WriteString(fmt.Sprintf("  <title>%s</title>\n", xmlEscape(entry.Title)))
	for _, css := range cssDests {
		sb.WriteString(fmt.Sprintf(`  <link href="../Styles/%s" rel="stylesheet" type="text/css"/>`, css) + "\n")
	}
	sb.WriteString("</head>\n<body>\n")
	for i, body := range bodies {
		if i > 0 {
			sb.WriteString("\n<hr style=\"margin: 3em 10%; border: none; border-top: 1px solid #888;\"/>\n")
		}
		sb.Write(body)
		sb.WriteString("\n")
	}
	sb.WriteString("</body>\n</html>")
	return []byte(sb.String()), nil
}

func extractBodyContent(html []byte) []byte {
	bodyStart := bytes.Index(html, []byte("<body"))
	if bodyStart < 0 {
		return html
	}
	tagEnd := bytes.IndexByte(html[bodyStart:], '>')
	if tagEnd < 0 {
		return html
	}
	contentStart := bodyStart + tagEnd + 1
	bodyEnd := bytes.LastIndex(html, []byte("</body>"))
	if bodyEnd < 0 || bodyEnd <= contentStart {
		return html
	}
	return bytes.TrimSpace(html[contentStart:bodyEnd])
}

func appendAfterBodyTag(src, insert []byte) []byte {
	idx := bytes.Index(src, []byte("<body"))
	if idx < 0 {
		return append(insert, src...)
	}
	end := bytes.IndexByte(src[idx:], '>')
	if end < 0 {
		return append(insert, src...)
	}
	end += idx + 1
	result := make([]byte, 0, len(src)+len(insert)+1)
	result = append(result, src[:end]...)
	result = append(result, '\n')
	result = append(result, insert...)
	result = append(result, src[end:]...)
	return result
}

// ---------------------------------------------------------------------------
// Validate command -- dry run that checks a config against source epubs
// ---------------------------------------------------------------------------

// runValidate checks a config against the provided source epubs without
// producing any output. It reports missing chapters, duplicate entries, and
// other problems.
func runValidate(sources map[string]string, cfg *Config) error {
	books := cfg.effectiveBooks()

	// Check that sources are provided for every book ID referenced.
	usedBooks := map[string]bool{}
	for _, ch := range cfg.Chapters {
		if ch.IsCombined() {
			for _, p := range ch.Parts {
				usedBooks[p.Book] = true
			}
		} else {
			usedBooks[ch.Book] = true
		}
	}
	for _, fm := range cfg.FrontMatter {
		usedBooks[fm.Book] = true
	}

	var problems []string

	for id := range usedBooks {
		if sources[id] == "" {
			problems = append(problems, fmt.Sprintf("  no source epub provided for book ID %q", id))
		}
	}

	// Open all source epubs.
	zipReaders := make(map[string]*zip.ReadCloser)
	zipIndexes := make(map[string]map[string]*zip.File)
	for id, p := range sources {
		rc, err := zip.OpenReader(p)
		if err != nil {
			problems = append(problems, fmt.Sprintf("  cannot open %s epub (%s): %v", id, p, err))
			continue
		}
		defer rc.Close()
		zipReaders[id] = rc
		zipIndexes[id] = zipIndex(&rc.Reader)
	}

	// Run auto-detect for books that request it.
	for id, bk := range books {
		if !bk.AutoDetect {
			continue
		}
		rc, ok := zipReaders[id]
		if !ok {
			continue
		}
		detected, err := autoDetectBookConfig(id, &rc.Reader, bk)
		if err != nil {
			problems = append(problems, fmt.Sprintf("  auto-detect failed for %s: %v", id, err))
			continue
		}
		books[id] = detected
	}

	// Resolve spine paths for books that request it.
	for id, bk := range books {
		if !bk.UseSpine {
			continue
		}
		rc, ok := zipReaders[id]
		if !ok {
			continue
		}
		paths, err := resolveSpinePaths(&rc.Reader)
		if err != nil {
			problems = append(problems, fmt.Sprintf("  spine resolution failed for %s: %v", id, err))
			continue
		}
		bk.ChapterPaths = paths
		books[id] = bk
	}

	// Validate front matter.
	for _, fm := range cfg.FrontMatter {
		idx, ok := zipIndexes[fm.Book]
		if !ok {
			continue // Already flagged above.
		}
		if _, ok := idx[fm.File]; !ok {
			problems = append(problems, fmt.Sprintf("  front matter %q (%s) -> %s not found", fm.Title, fm.Book, fm.File))
		}
	}

	// Validate chapters and check for duplicates.
	type chapterKey struct {
		Book string
		Num  int
	}
	seen := map[chapterKey]string{}

	for _, entry := range cfg.Chapters {
		check := func(bookID string, num int, title string) {
			idx, ok := zipIndexes[bookID]
			if !ok {
				return // Already flagged.
			}
			p := resolveChapterPath(bookID, num, title, books)
			if strings.HasPrefix(p, "OUT_OF_RANGE:") {
				problems = append(problems, fmt.Sprintf("  %q (%s ch%d) -> %s", title, bookID, num, p))
			} else if strings.HasPrefix(p, "NON_XHTML:") {
				problems = append(problems, fmt.Sprintf("  %q (%s ch%d) -> %s (non-XHTML spine entry)", title, bookID, num, p))
			} else if _, ok := idx[p]; !ok {
				problems = append(problems, fmt.Sprintf("  %q (%s ch%d) -> %s not found in zip", title, bookID, num, p))
			}

			key := chapterKey{bookID, num}
			if prev, dup := seen[key]; dup {
				problems = append(problems, fmt.Sprintf("  %q (%s ch%d) duplicates earlier entry %q", entry.Title, bookID, num, prev))
			}
			seen[key] = entry.Title
		}

		if entry.IsCombined() {
			for _, part := range entry.Parts {
				check(part.Book, part.Num, "")
			}
		} else {
			check(entry.Book, entry.Num, entry.Title)
		}
	}

	// Summary.
	total := len(cfg.Chapters)
	fmt.Printf("Config:   %s\n", cfg.Name)
	fmt.Printf("Chapters: %d\n", total)
	fmt.Printf("Books:    %d referenced", len(usedBooks))
	ids := make([]string, 0, len(usedBooks))
	for id := range usedBooks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fmt.Printf(" (%s)\n", strings.Join(ids, ", "))

	if len(problems) == 0 {
		fmt.Printf("\nAll %d chapters validated successfully.\n", total)
		return nil
	}
	return fmt.Errorf("%d problem(s) found:\n%s", len(problems), strings.Join(problems, "\n"))
}

// ---------------------------------------------------------------------------
// Audio builder
// ---------------------------------------------------------------------------

// collectAudioFiles returns a sorted list of audio file paths. path may be
// a directory (all audio files within it are returned) or a single file path.
func collectAudioFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		return []string{abs}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".mp3" || ext == ".m4b" || ext == ".m4a" ||
			ext == ".flac" || ext == ".ogg" || ext == ".opus" {
			abs, err := filepath.Abs(filepath.Join(path, e.Name()))
			if err != nil {
				return nil, err
			}
			files = append(files, abs)
		}
	}
	sort.Strings(files)
	return files, nil
}

// LogicalChapter represents one output chapter, which may be sourced from one
// or more consecutive audio segments that share the same title across files.
type LogicalChapter struct {
	Title    string
	Segments []AudioSegment
}

// DurSec returns the total duration of all source segments.
func (lc LogicalChapter) DurSec() float64 {
	var total float64
	for _, s := range lc.Segments {
		total += s.DurSec()
	}
	return total
}

// collectAudioSegments probes all files in paths for embedded chapter metadata
// and returns a flat ordered list of logical chapters. Chapters shorter than
// minDurSec or whose lowercase title appears in skipTitles are omitted.
// Consecutive segments sharing the same title are merged into one logical
// chapter, which handles editions that split a chapter across multiple tracks
// or across file boundaries.
func collectAudioSegments(paths []string, minDurSec float64, skipTitles map[string]bool) ([]LogicalChapter, error) {
	var result []LogicalChapter
	for _, p := range paths {
		segs, err := probeChapters(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		for _, s := range segs {
			if s.DurSec() < minDurSec {
				continue
			}
			if skipTitles[strings.ToLower(strings.TrimSpace(s.Title))] {
				continue
			}
			// Merge into the previous logical chapter if it shares the same
			// title. This correctly handles cross-file splits by appending the
			// new segment rather than extending the previous one's EndSec.
			if len(result) > 0 && strings.EqualFold(result[len(result)-1].Title, s.Title) {
				result[len(result)-1].Segments = append(result[len(result)-1].Segments, s)
				continue
			}
			result = append(result, LogicalChapter{
				Title:    s.Title,
				Segments: []AudioSegment{s},
			})
		}
	}
	return result, nil
}

// povName strips a trailing roman numeral so "Tyrion III" becomes "Tyrion",
// matching how audiobook editions typically label repeated POV characters.
func povName(title string) string {
	parts := strings.Fields(title)
	if len(parts) > 1 && isRomanNumeral(parts[len(parts)-1]) {
		return strings.Join(parts[:len(parts)-1], " ")
	}
	return title
}

// runAudio builds a spliced M4B. sources maps each book ID to one or more
// file or directory paths containing its audio. Multiple paths are probed in
// order and their chapters concatenated, which handles multi-part audiobooks.
func runAudio(sources map[string][]string, outputPath string, cfg *Config) error {
	books := cfg.effectiveBooks()
	skip := map[string]bool{"intro": true, "credits": true}

	// Collect segments per book ID, preserving insertion order from config.
	segsPerBook := make(map[string][]LogicalChapter)
	var orderedIDs []string
	seen := map[string]bool{}
	for _, ch := range cfg.Chapters {
		ids := []string{ch.Book}
		if ch.IsCombined() {
			ids = nil
			for _, p := range ch.Parts {
				ids = append(ids, p.Book)
			}
		}
		for _, id := range ids {
			if seen[id] || len(sources[id]) == 0 {
				continue
			}
			seen[id] = true
			orderedIDs = append(orderedIDs, id)
		}
	}

	for _, id := range orderedIDs {
		paths := sources[id]
		var allFiles []string
		for _, p := range paths {
			files, err := collectAudioFiles(p)
			if err != nil {
				return fmt.Errorf("reading %s audio (%s): %w", id, p, err)
			}
			allFiles = append(allFiles, files...)
		}
		logf("Probing %s chapters...\n", id)
		segs, err := collectAudioSegments(allFiles, 30.0, skip)
		if err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		logf("  %d chapters found\n", len(segs))
		bk := books[id]
		if bk.ExpectedChapters > 0 && len(segs) != bk.ExpectedChapters {
			logf("  WARNING: expected %d %s chapters, got %d\n", bk.ExpectedChapters, id, len(segs))
		}
		segsPerBook[id] = segs
	}

	getSegment := func(bookID string, num int, title string) (LogicalChapter, error) {
		segs, ok := segsPerBook[bookID]
		if !ok {
			return LogicalChapter{}, fmt.Errorf("no audio loaded for book %q", bookID)
		}
		idx := num - 1
		if idx < 0 || idx >= len(segs) {
			return LogicalChapter{}, fmt.Errorf("%s chapter %d (index %d) out of range (have %d segments)",
				bookID, num, idx, len(segs))
		}
		lc := segs[idx]
		expected := povName(title)
		if !strings.EqualFold(lc.Title, expected) &&
			!strings.Contains(strings.ToLower(lc.Title), strings.ToLower(expected)) &&
			!strings.Contains(strings.ToLower(expected), strings.ToLower(lc.Title)) {
			logf("  NOTE: %s ch%d -- config title %q, audio title %q\n", bookID, num, expected, lc.Title)
		}
		return lc, nil
	}

	logf("Validating chapter mapping...\n")
	for _, ch := range cfg.Chapters {
		if ch.IsCombined() {
			if _, err := getSegment(ch.Parts[0].Book, ch.Parts[0].Num, ch.Title); err != nil {
				return err
			}
		} else {
			if _, err := getSegment(ch.Book, ch.Num, ch.Title); err != nil {
				return err
			}
		}
	}

	tmpDir, err := os.MkdirTemp(filepath.Dir(outputPath), "feast-with-dragons-audio-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	concatPath := filepath.Join(tmpDir, "concat.txt")
	metaPath := filepath.Join(tmpDir, "chapters.txt")

	concatFile, err := os.Create(concatPath)
	if err != nil {
		return err
	}
	metaFile, err := os.Create(metaPath)
	if err != nil {
		return err
	}

	author := cfg.Author
	if author == "" {
		author = "Unknown"
	}
	fmt.Fprintf(metaFile, ";FFMETADATA1\ntitle=%s\nartist=%s\n\n", cfg.Name, author)

	// Pass 1: extract each chapter segment to a temp file. This resets
	// timestamps to zero for each segment, which prevents non-monotonic DTS
	// warnings when ffmpeg encounters segments from different positions within
	// large source files.
	logf("Extracting chapters...\n")
	var offsetMs int64
	for i, ch := range cfg.Chapters {
		bookID, num := ch.Book, ch.Num
		if ch.IsCombined() {
			bookID, num = ch.Parts[0].Book, ch.Parts[0].Num
		}
		lc, _ := getSegment(bookID, num, ch.Title)

		for j, seg := range lc.Segments {
			segPath := filepath.Join(tmpDir, fmt.Sprintf("seg_%04d_%02d.m4a", i, j))
			err := runCommandSilent("ffmpeg",
				"-y",
				"-ss", fmt.Sprintf("%f", seg.StartSec),
				"-to", fmt.Sprintf("%f", seg.EndSec),
				"-i", seg.File,
				"-map", "0:a",
				"-c", "copy",
				"-avoid_negative_ts", "make_zero",
				segPath,
			)
			if err != nil {
				return fmt.Errorf("extracting %s ch%d segment %d: %w", bookID, num, j, err)
			}
			escaped := strings.ReplaceAll(segPath, `\`, `\\`)
			escaped = strings.ReplaceAll(escaped, "'", `\'`)
			fmt.Fprintf(concatFile, "file '%s'\n", escaped)
		}

		durMs := int64(lc.DurSec() * 1000)
		start := offsetMs
		offsetMs += durMs
		fmt.Fprintf(metaFile, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n\n",
			start, offsetMs, ch.Title)

		logf("  [%03d/%d] %s (%.1fs, %d segment(s))\n",
			i+1, len(cfg.Chapters), ch.Title, lc.DurSec(), len(lc.Segments))
	}
	concatFile.Close()
	metaFile.Close()

	// Pass 2: concatenate the extracted segments with the chapter metadata.
	logf("\nRunning ffmpeg -> %s\n", outputPath)
	return runCommand("ffmpeg",
		"-y",
		"-f", "concat",
		"-safe", "0",
		"-i", concatPath,
		"-i", metaPath,
		"-map", "0:a",
		"-map_metadata", "1",
		"-map_chapters", "1",
		"-c", "copy",
		outputPath,
	)
}

// ---------------------------------------------------------------------------
// Merge command -- whole-book concatenation without chapter-level splicing
// ---------------------------------------------------------------------------

// runMergeAudio concatenates audio files in order, preserving any embedded
// chapter markers from each source file.
func runMergeAudio(inputPaths []string, outputPath, title, author string) error {
	tmpDir, err := os.MkdirTemp("", "feast-with-dragons-merge-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	concatPath := filepath.Join(tmpDir, "concat.txt")
	metaPath := filepath.Join(tmpDir, "chapters.txt")

	concatFile, err := os.Create(concatPath)
	if err != nil {
		return err
	}
	metaFile, err := os.Create(metaPath)
	if err != nil {
		return err
	}

	if author == "" {
		author = "Unknown"
	}
	fmt.Fprintf(metaFile, ";FFMETADATA1\ntitle=%s\nartist=%s\n\n", title, author)

	var offsetMs int64
	for _, p := range inputPaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		segs, err := probeChapters(abs)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		logf("  %s: %d chapters\n", filepath.Base(p), len(segs))

		escaped := strings.ReplaceAll(abs, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, "'", `\'`)
		fmt.Fprintf(concatFile, "file '%s'\n", escaped)

		for _, seg := range segs {
			durMs := int64(seg.DurSec() * 1000)
			start := offsetMs
			offsetMs += durMs
			fmt.Fprintf(metaFile, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n\n",
				start, offsetMs, seg.Title)
		}
	}
	concatFile.Close()
	metaFile.Close()

	logf("\nRunning ffmpeg -> %s\n", outputPath)
	return runCommand("ffmpeg",
		"-y",
		"-f", "concat",
		"-safe", "0",
		"-i", concatPath,
		"-i", metaPath,
		"-map", "0:a",
		"-map_metadata", "1",
		"-map_chapters", "1",
		"-c", "copy",
		outputPath,
	)
}

// runMergeEpub concatenates epub files in order into a single output epub.
// No chapter-level splicing is performed; the spine of each source book is
// appended in sequence.
func runMergeEpub(inputPaths []string, outputPath, title, author string) error {
	if author == "" {
		author = "Unknown"
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	out := zip.NewWriter(outFile)
	defer out.Close()

	cw := &checkedZipWriter{w: out}

	mh := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	mw, err := out.CreateHeader(mh)
	if err != nil {
		return fmt.Errorf("writing mimetype: %w", err)
	}
	if _, err := mw.Write([]byte("application/epub+zip")); err != nil {
		return fmt.Errorf("writing mimetype: %w", err)
	}
	cw.write("META-INF/container.xml", []byte(containerXML))

	var manifestItems, spineItems, navPoints []string
	playOrder := 1
	chapterNum := 0
	writtenPaths := map[string]bool{}

	for bookIdx, p := range inputPaths {
		if err := func() error {
			rc, err := zip.OpenReader(p)
			if err != nil {
				return fmt.Errorf("opening %s: %w", filepath.Base(p), err)
			}
			defer rc.Close()

			idx := zipIndex(&rc.Reader)
			prefix := fmt.Sprintf("book%02d", bookIdx+1)

			// Copy all content files with a per-book prefix to avoid collisions.
			for name, f := range idx {
				if name == "mimetype" || strings.HasPrefix(name, "META-INF") {
					continue
				}
				data, err := readZipEntry(f)
				if err != nil {
					continue
				}
				dest := prefix + "/" + name
				if !writtenPaths[dest] {
					cw.write(dest, data)
					writtenPaths[dest] = true
				}
			}

			// Parse OPF to get spine order and manifest.
			var opfPath string
			for name := range idx {
				if strings.HasSuffix(name, ".opf") {
					opfPath = name
					break
				}
			}
			if opfPath == "" {
				logf("  WARNING: no OPF found in %s, skipping\n", filepath.Base(p))
				return nil
			}

			opfData, err := readZipEntry(idx[opfPath])
			if err != nil {
				return fmt.Errorf("reading OPF from %s: %w", filepath.Base(p), err)
			}

			type opfItem struct {
				ID        string `xml:"id,attr"`
				Href      string `xml:"href,attr"`
				MediaType string `xml:"media-type,attr"`
			}
			type opfItemRef struct {
				IDRef string `xml:"idref,attr"`
			}
			type opfManifest struct {
				Items []opfItem `xml:"item"`
			}
			type opfSpine struct {
				ItemRefs []opfItemRef `xml:"itemref"`
			}
			type opfPackage struct {
				Manifest opfManifest `xml:"manifest"`
				Spine    opfSpine    `xml:"spine"`
			}
			var pkg opfPackage
			if err := xml.Unmarshal(opfData, &pkg); err != nil {
				return fmt.Errorf("parsing OPF from %s: %w", filepath.Base(p), err)
			}

			// Build a map from item ID to href.
			itemHrefs := map[string]string{}
			for _, item := range pkg.Manifest.Items {
				itemHrefs[item.ID] = item.Href
			}

			// Add spine items to the combined spine. Use path (not filepath)
			// for zip-internal directory resolution.
			opfDir := path.Dir(opfPath)
			for _, ref := range pkg.Spine.ItemRefs {
				href := itemHrefs[ref.IDRef]
				if href == "" {
					continue
				}
				chapterNum++
				id := fmt.Sprintf("ch_%04d", chapterNum)
				destHref := prefix + "/" + opfDir + "/" + href
				manifestItems = append(manifestItems,
					fmt.Sprintf(`    <item id="%s" href="%s" media-type="application/xhtml+xml"/>`, id, destHref))
				spineItems = append(spineItems, fmt.Sprintf(`    <itemref idref="%s"/>`, id))
				navPoints = append(navPoints, fmt.Sprintf(
					"    <navPoint id=\"np-%d\" playOrder=\"%d\">\n      <navLabel><text>%s ch%d</text></navLabel>\n      <content src=\"%s\"/>\n    </navPoint>",
					playOrder, playOrder, filepath.Base(p), chapterNum, destHref))
				playOrder++
			}

			logf("  %s: %d spine items\n", filepath.Base(p), len(pkg.Spine.ItemRefs))
			return nil
		}(); err != nil {
			return err
		}
	}

	bookID := sanitiseID(strings.ToLower(title))
	cw.write("OEBPS/content.opf",
		[]byte(buildOPF(bookID, title, author, manifestItems, spineItems)))
	cw.write("OEBPS/toc.ncx",
		[]byte(buildNCX(bookID, title, navPoints)))

	if cw.err != nil {
		return fmt.Errorf("writing epub contents: %w", cw.err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("finalising epub: %w", err)
	}
	info, _ := os.Stat(outputPath)
	logf("\nDone. Output: %s (%.1f MB)\n", outputPath, float64(info.Size())/1_048_576)
	return nil
}

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

func binaryName() string {
	return filepath.Base(os.Args[0])
}

// autoDetect returns the first file in dir whose lowercase name contains all
// of keywords and ends with ext. Returns empty string if nothing matches.
func autoDetect(dir, ext string, keywords ...string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if !strings.HasSuffix(name, ext) {
			continue
		}
		allMatch := true
		for _, kw := range keywords {
			if !strings.Contains(name, strings.ToLower(kw)) {
				allMatch = false
				break
			}
		}
		if allMatch {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// autoDetectAudioFiles returns all audio files in dir whose lowercase names
// contain all of keywords, sorted by filename. Used to collect multi-part
// audiobooks (e.g. a four-part ADWD) from a directory that also contains
// files for other books.
func autoDetectAudioFiles(dir string, keywords ...string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []string
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		ext := filepath.Ext(name)
		if ext != ".m4b" && ext != ".m4a" && ext != ".mp3" &&
			ext != ".flac" && ext != ".ogg" && ext != ".opus" {
			continue
		}
		allMatch := true
		for _, kw := range keywords {
			if !strings.Contains(name, strings.ToLower(kw)) {
				allMatch = false
				break
			}
		}
		if allMatch {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(found)
	return found
}

// parseBookFlags parses a slice of "id=path" strings into a map.
func parseBookFlags(pairs []string) (map[string]string, error) {
	m := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid -book flag %q: expected id=path", pair)
		}
		m[parts[0]] = parts[1]
	}
	return m, nil
}

// repeatable is a flag.Value that collects multiple string values.
type repeatable []string

func (r *repeatable) String() string  { return strings.Join(*r, ", ") }
func (r *repeatable) Set(v string) error { *r = append(*r, v); return nil }

func printUsage() {
	bin := binaryName()
	fmt.Printf(`%s -- splice multiple epub or audio files into a custom reading order

Usage:
  %s ebook     [flags]
  %s audio     [flags]
  %s merge     [flags] file1 file2 ...
  %s scan      [-init <config.json>] [-id <BOOKID>] <epub>
  %s validate  [flags]

Subcommands:
  ebook     Build a spliced epub from source epubs.
  audio     Build a spliced M4B from source audio files.
  merge     Concatenate whole books without chapter-level splicing.
  scan      Print the spine contents of an epub (useful for authoring configs).
  validate  Dry-run a config against source epubs without producing output.

Built-in splicings (-splicing flag):
  fwd     A Feast with Dragons  (default)
  boiled  Boiled Leather
  ball    A Ball of Beasts
  <path>  Path to a custom JSON config file

Common flags (ebook, audio, merge):
  -quiet               Suppress progress output
  -force               Overwrite existing output file

Ebook flags:
  -splicing <n>        Splicing to use (default: fwd)
  -affc <path>         Path to A Feast for Crows epub (auto-detected if omitted)
  -adwd <path>         Path to A Dance with Dragons epub (auto-detected if omitted)
  -book <id>=<path>    Source epub for a book ID; repeatable for multiple books
  -out  <path>         Output file (default: <splicing name>.epub)
  -annotate            Add a source book annotation to each chapter

Audio flags:
  -splicing <n>        Splicing to use (default: fwd)
  -affc <dir>          Directory containing AFFC audio files
  -adwd <dir>          Directory containing ADWD audio files
  -book <id>=<dir>     Audio directory for a book ID; repeatable
  -out  <path>         Output file (default: <splicing name>.m4b)

Merge flags:
  -title  <string>     Title for the output file
  -author <string>     Author name written into metadata
  -out    <path>       Output file (required)

Validate flags:
  -splicing <n>        Splicing to validate (default: fwd)
  -affc <path>         Path to AFFC epub (auto-detected if omitted)
  -adwd <path>         Path to ADWD epub (auto-detected if omitted)
  -book <id>=<path>    Source epub for a book ID; repeatable

Scan flags:
  -init <path>         Write a skeleton JSON config instead of printing the spine
  -id   <BOOKID>       Book ID to use in the generated config (default: MYBOOK)

The merge subcommand determines output format from the -out extension:
  .epub  -- epub concatenation
  .m4b / .mp3 / .m4a  -- audio concatenation via ffmpeg

Audio matching is positional: book chapter N maps to the Nth chapter segment
found in the audio directory, sorted by filename. Title matching is used for
sanity checks only and does not affect the output.

Accepted audio formats: mp3, m4b, m4a, flac, ogg, opus.
Audio subcommands require ffmpeg and ffprobe on PATH.

Custom splicings:
  Any JSON file can be passed to -splicing. See the built-in configs in the
  configs/ directory for the schema. The "books" key is optional and falls
  back to built-in defaults for AFFC and ADWD.
`, bin, bin, bin, bin, bin, bin)
}

// checkOutputExists prints an error and exits if the output file exists and
// --force is not set. With --force it removes the existing file.
func checkOutputExists(path string) {
	if _, err := os.Stat(path); err != nil {
		return // Does not exist, fine.
	}
	if forceMode {
		os.Remove(path)
		return
	}
	fmt.Fprintf(os.Stderr, "Output file already exists: %s\nRemove it, use -out to specify a different path, or pass -force to overwrite.\n", path)
	os.Exit(1)
}

// addCommonFlags registers -quiet and -force on a FlagSet and wires them to
// the package-level variables after parsing.
func addCommonFlags(fs *flag.FlagSet) {
	fs.BoolVar(&quietMode, "quiet", false, "Suppress progress output")
	fs.BoolVar(&forceMode, "force", false, "Overwrite existing output file")
}

func main() {
	cwd, _ := os.Getwd()

	if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-") {
		os.Args = append([]string{os.Args[0], "ebook"}, os.Args[1:]...)
	}

	switch os.Args[1] {
	case "ebook":
		fs := flag.NewFlagSet("ebook", flag.ExitOnError)
		affcFlag     := fs.String("affc", "", "Path to AFFC epub (auto-detected if omitted)")
		adwdFlag     := fs.String("adwd", "", "Path to ADWD epub (auto-detected if omitted)")
		outFlag      := fs.String("out", "", "Output path (default: <splicing name>.epub)")
		splicingFlag := fs.String("splicing", "fwd", "Splicing to use")
		annotateFlag := fs.Bool("annotate", false, "Add source book annotation to each chapter")
		addCommonFlags(fs)
		var bookFlags repeatable
		fs.Var(&bookFlags, "book", "id=path for a source epub; repeatable")
		fs.Parse(os.Args[2:])

		cfg, err := loadConfig(*splicingFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}

		sources, err := parseBookFlags(bookFlags)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// -affc and -adwd are convenience aliases.
		if *affcFlag != "" {
			sources["AFFC"] = *affcFlag
		}
		if *adwdFlag != "" {
			sources["ADWD"] = *adwdFlag
		}
		outPath := *outFlag
		if outPath == "" {
			outPath = cfg.Name + ".epub"
		}

		checkOutputExists(outPath)

		if sources["AFFC"] == "" {
			if p := autoDetect(cwd, ".epub", "feast", "crows"); p != "" {
				sources["AFFC"] = p
			}
		}
		if sources["ADWD"] == "" {
			if p := autoDetect(cwd, ".epub", "dance", "dragons"); p != "" {
				sources["ADWD"] = p
			}
		}

		if err := runEbook(sources, outPath, cfg, *annotateFlag); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "audio":
		fs := flag.NewFlagSet("audio", flag.ExitOnError)
		affcFlag     := fs.String("affc", "", "File or directory for AFFC audio (auto-detected if omitted)")
		adwdFlag     := fs.String("adwd", "", "File or directory for ADWD audio (auto-detected if omitted)")
		outFlag      := fs.String("out", "", "Output path (default: <splicing name>.m4b)")
		splicingFlag := fs.String("splicing", "fwd", "Splicing to use")
		addCommonFlags(fs)
		var bookFlags repeatable
		fs.Var(&bookFlags, "book", "id=file-or-dir for a book's audio; repeatable")
		fs.Parse(os.Args[2:])

		cfg, err := loadConfig(*splicingFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}

		outPath := *outFlag
		if outPath == "" {
			outPath = cfg.Name + ".m4b"
		}

		checkOutputExists(outPath)

		rawSources, err := parseBookFlags(bookFlags)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		// Convert flat map to []string map.
		sources := make(map[string][]string, len(rawSources))
		for id, p := range rawSources {
			sources[id] = []string{p}
		}

		if *affcFlag != "" {
			sources["AFFC"] = []string{*affcFlag}
		}
		if *adwdFlag != "" {
			sources["ADWD"] = []string{*adwdFlag}
		}
		if len(sources["AFFC"]) == 0 {
			if found := autoDetectAudioFiles(cwd, "feast", "crows"); len(found) > 0 {
				sources["AFFC"] = found
				logf("Auto-detected AFFC audio: %v\n", found)
			}
		}
		if len(sources["ADWD"]) == 0 {
			if found := autoDetectAudioFiles(cwd, "dance", "dragons"); len(found) > 0 {
				sources["ADWD"] = found
				logf("Auto-detected ADWD audio: %v\n", found)
			}
		}

		if err := runAudio(sources, outPath, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "merge":
		fs := flag.NewFlagSet("merge", flag.ExitOnError)
		titleFlag  := fs.String("title", "Merged", "Output title")
		authorFlag := fs.String("author", "", "Author name for metadata")
		outFlag    := fs.String("out", "", "Output file path (required)")
		addCommonFlags(fs)
		fs.Parse(os.Args[2:])

		args := fs.Args()
		if len(args) < 2 || *outFlag == "" {
			fmt.Fprintf(os.Stderr, "Usage: %s merge -out <output> [-title <title>] [-author <author>] file1 file2 ...\n", binaryName())
			os.Exit(1)
		}


		checkOutputExists(*outFlag)
		ext := strings.ToLower(filepath.Ext(*outFlag))
		var mergeErr error
		switch ext {
		case ".epub":
			mergeErr = runMergeEpub(args, *outFlag, *titleFlag, *authorFlag)
		case ".m4b", ".mp3", ".m4a":
			mergeErr = runMergeAudio(args, *outFlag, *titleFlag, *authorFlag)
		default:
			fmt.Fprintf(os.Stderr, "Cannot determine format from output extension %q. Use .epub, .m4b, .m4a, or .mp3.\n", ext)
			os.Exit(1)
		}
		if mergeErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", mergeErr)
			os.Exit(1)
		}

	case "scan":
		fs := flag.NewFlagSet("scan", flag.ExitOnError)
		initFlag := fs.String("init", "", "Write a skeleton JSON config to this path instead of printing the spine")
		bookIDFlag := fs.String("id", "MYBOOK", "Book ID to use in the generated config (only with -init)")
		fs.Parse(os.Args[2:])
		args := fs.Args()
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "Usage: %s scan [-init <config.json>] [-id <BOOKID>] <epub>\n", binaryName())
			os.Exit(1)
		}
		if *initFlag != "" {
			if err := runScanInit(args[0], *initFlag, *bookIDFlag); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else {
			if err := runScan(args[0]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

	case "validate":
		fs := flag.NewFlagSet("validate", flag.ExitOnError)
		affcFlag     := fs.String("affc", "", "Path to AFFC epub (auto-detected if omitted)")
		adwdFlag     := fs.String("adwd", "", "Path to ADWD epub (auto-detected if omitted)")
		splicingFlag := fs.String("splicing", "fwd", "Splicing to use")
		var bookFlags repeatable
		fs.Var(&bookFlags, "book", "id=path for a source epub; repeatable")
		fs.Parse(os.Args[2:])

		cfg, err := loadConfig(*splicingFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}

		sources, err := parseBookFlags(bookFlags)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if *affcFlag != "" {
			sources["AFFC"] = *affcFlag
		}
		if *adwdFlag != "" {
			sources["ADWD"] = *adwdFlag
		}
		if sources["AFFC"] == "" {
			if p := autoDetect(cwd, ".epub", "feast", "crows"); p != "" {
				sources["AFFC"] = p
			}
		}
		if sources["ADWD"] == "" {
			if p := autoDetect(cwd, ".epub", "dance", "dragons"); p != "" {
				sources["ADWD"] = p
			}
		}

		if err := runValidate(sources, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	case "help", "-help", "--help", "-h":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}
