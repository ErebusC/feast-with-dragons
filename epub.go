package main

import (
	"archive/zip"
	"fmt"
	"os"
	"path"
	"strings"
)

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
//     roman numerals are deliberately preserved to avoid false matches.
//
// Note: this function intentionally does NOT use normalizeTitle from scan.go.
// normalizeTitle strips trailing roman numerals for fuzzy indexing, which is
// correct when building the NCX lookup table but would cause "Jon I" and
// "Jon II" to resolve to the same entry at lookup time.
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
// Ebook builder
// ---------------------------------------------------------------------------

// runEbook builds a spliced epub. sources maps each book ID used in cfg to
// the path of its source epub file. If annotate is true, a small source
// annotation is appended to each chapter indicating which book it came from.
func runEbook(sources map[string]string, outputPath string, cfg *Config, annotate, numberedTOC bool) error {
	books := cfg.effectiveBooks()
	usedBookIDs, _ := cfg.UsedBookIDs()

	books, zipReaders, zipIndexes, err := prepareBooks(sources, books)
	if err != nil {
		return err
	}
	for _, rc := range zipReaders {
		defer rc.Close()
	}

	// Validate all chapters and front matter are present before writing.
	logf("Validating source chapters...\n")
	var missing []string
	check := func(bookID string, num int, title string) {
		idx, ok := zipIndexes[bookID]
		if !ok {
			missing = append(missing, fmt.Sprintf("  %q (%s ch%d) -> no source epub provided", title, bookID, num))
			return
		}
		p := resolveChapterPath(bookID, num, title, books)
		if strings.HasPrefix(p, "OUT_OF_RANGE:") {
			missing = append(missing, fmt.Sprintf("  %q (%s ch%d) -> %s -- num exceeds spine length", title, bookID, num, p))
			return
		}
		if strings.HasPrefix(p, "NON_XHTML:") {
			missing = append(missing, fmt.Sprintf("  %q (%s ch%d) -> %s -- non-XHTML spine entry", title, bookID, num, p))
			return
		}
		if _, ok := idx[p]; !ok {
			missing = append(missing, fmt.Sprintf("  %q (%s ch%d) -> %s not found", title, bookID, num, p))
		}
	}
	for _, fm := range cfg.FrontMatter {
		idx, ok := zipIndexes[fm.Book]
		if !ok {
			missing = append(missing, fmt.Sprintf("  front matter %q (%s) -> no source epub provided", fm.Title, fm.Book))
			continue
		}
		if _, ok := idx[fm.File]; !ok {
			missing = append(missing, fmt.Sprintf("  front matter %q (%s) -> %s not found", fm.Title, fm.Book, fm.File))
		}
	}
	for _, entry := range cfg.Chapters {
		if entry.IsCombined() {
			for _, part := range entry.Parts {
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

	cw, err := startEpubZip(out)
	if err != nil {
		return err
	}

	// Stylesheets and images -- one pass per source book.
	var manifestItems []string

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
				if !strings.HasPrefix(name, bk.ImageSrcPrefix) {
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
	cw.write("OEBPS/Text/cover.html", []byte(buildCoverHTML(cfg.Name, series, author)))

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
		html := rewriteHTML(raw, bk)
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
	cw.write("OEBPS/Text/toc.html", []byte(buildTOCHTML(cfg.Name, cfg.Chapters, numberedTOC)))
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
			chHTML = rewriteHTML(raw, books[entry.Book])
			logf("  [%03d/%d] %s (%s ch%d)\n", pos, len(cfg.Chapters), entry.Title, entry.Book, entry.Num)
		}

		if annotate {
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
			chHTML = insertBeforeBodyClose(chHTML, []byte(banner))
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
		[]byte(buildOPF(bookID, cfg.Name, author, manifestItems, spineItems, len(cfg.Chapters) > 0)))
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

// ---------------------------------------------------------------------------
// Combined chapters
// ---------------------------------------------------------------------------

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
		html := rewriteHTML(raw, bk)
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

// ---------------------------------------------------------------------------
// Embedded HTML templates
// ---------------------------------------------------------------------------

func buildCoverHTML(title, series, author string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
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
		xmlEscape(title),
		xmlEscape(title),
		xmlEscape(title),
		xmlEscape(series),
		xmlEscape(author),
	)
}

func buildTOCHTML(title string, chapters []ChapterEntry, numbered bool) string {
	var tocRows strings.Builder
	for i, entry := range chapters {
		label := xmlEscape(entry.Title)
		if numbered {
			label = fmt.Sprintf("%d. %s", i+1, label)
		}
		tocRows.WriteString(fmt.Sprintf(
			"      <li><a href=\"chapter_%03d.html\">%s</a></li>\n",
			i+1, label))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
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
</html>`, xmlEscape(title), tocRows.String())
}
