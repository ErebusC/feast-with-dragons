package main

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// OPF / container XML structs
// ---------------------------------------------------------------------------

type xmlContainer struct {
	Rootfiles []xmlRootfile `xml:"rootfiles>rootfile"`
}
type xmlRootfile struct {
	FullPath string `xml:"full-path,attr"`
}

type xmlPackage struct {
	Metadata xmlMetadata `xml:"metadata"`
	Manifest xmlManifest `xml:"manifest"`
	Spine    xmlSpine    `xml:"spine"`
}
type xmlMetadata struct {
	Meta []xmlMeta `xml:"meta"`
}
type xmlMeta struct {
	Name    string `xml:"name,attr"`
	Content string `xml:"content,attr"`
}
type xmlManifest struct {
	Items []xmlItem `xml:"item"`
}
type xmlItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}
type xmlSpine struct {
	Items []xmlItemRef `xml:"itemref"`
}
type xmlItemRef struct {
	IDRef  string `xml:"idref,attr"`
	Linear string `xml:"linear,attr"`
}

// ---------------------------------------------------------------------------
// NCX XML structs (EPUB2)
// ---------------------------------------------------------------------------

type xmlNCX struct {
	NavMap xmlNavMap `xml:"navMap"`
}
type xmlNavMap struct {
	NavPoints []xmlNavPoint `xml:"navPoint"`
}
type xmlNavPoint struct {
	NavLabel  xmlNavLabel   `xml:"navLabel"`
	Content   xmlNavContent `xml:"content"`
	NavPoints []xmlNavPoint `xml:"navPoint"`
}
type xmlNavLabel struct {
	Text string `xml:"text"`
}
type xmlNavContent struct {
	Src string `xml:"src,attr"`
}

// ---------------------------------------------------------------------------
// readOPF -- shared helper
// ---------------------------------------------------------------------------

// readOPF parses the OPF from an open zip.Reader. It returns the OPF
// directory (used to resolve relative hrefs), the parsed package, a full
// zip index, and any error.
func readOPF(r *zip.Reader) (opfDir string, pkg xmlPackage, idx map[string]*zip.File, err error) {
	idx = zipIndex(r)

	containerData, err := readZipEntry(idx["META-INF/container.xml"])
	if err != nil {
		return "", pkg, nil, fmt.Errorf("reading container.xml: %w", err)
	}
	var container xmlContainer
	if err := xml.Unmarshal(containerData, &container); err != nil {
		return "", pkg, nil, fmt.Errorf("parsing container.xml: %w", err)
	}
	if len(container.Rootfiles) == 0 {
		return "", pkg, nil, fmt.Errorf("no rootfile found in container.xml")
	}
	opfPath := container.Rootfiles[0].FullPath
	opfDir = path.Dir(opfPath)

	opfData, err := readZipEntry(idx[opfPath])
	if err != nil {
		return "", pkg, nil, fmt.Errorf("reading OPF at %s: %w", opfPath, err)
	}
	if err := xml.Unmarshal(opfData, &pkg); err != nil {
		return "", pkg, nil, fmt.Errorf("parsing OPF: %w", err)
	}
	return opfDir, pkg, idx, nil
}

// ---------------------------------------------------------------------------
// normalizeTitle
// ---------------------------------------------------------------------------

var romanNumerals = map[string]bool{
	"i": true, "ii": true, "iii": true, "iv": true, "v": true,
	"vi": true, "vii": true, "viii": true, "ix": true, "x": true,
	"xi": true, "xii": true, "xiii": true, "xiv": true, "xv": true,
	"xvi": true, "xvii": true, "xviii": true, "xix": true, "xx": true,
}

// isRomanNumeral checks whether a string is a recognised roman numeral (case-insensitive).
func isRomanNumeral(s string) bool {
	return romanNumerals[strings.ToLower(s)]
}

// normalizeTitle produces a lowercase, stripped form of a chapter title
// for fuzzy matching against NCX nav labels. It strips parenthetical
// suffixes ("Prologue (Pate)" -> "prologue") and trailing roman numerals
// ("Jon I" -> "jon"). This lets config titles match NCX labels regardless
// of whether the NCX includes numerals.
func normalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.Index(s, "("); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	parts := strings.Fields(s)
	if len(parts) > 1 && romanNumerals[parts[len(parts)-1]] {
		s = strings.Join(parts[:len(parts)-1], " ")
	}
	return s
}

// ---------------------------------------------------------------------------
// readNCXIndex
// ---------------------------------------------------------------------------

// flattenNavPoints recursively collects all nav points from a navMap.
func flattenNavPoints(pts []xmlNavPoint) []xmlNavPoint {
	var out []xmlNavPoint
	for _, pt := range pts {
		out = append(out, pt)
		out = append(out, flattenNavPoints(pt.NavPoints)...)
	}
	return out
}

// readNCXIndex reads the NCX (EPUB2) or nav document (EPUB3) from an open
// epub and returns two maps:
//
//   - titleToPath: normalizeTitle(label) -> full zip path (fragment stripped)
//   - pathToTitle: full zip path -> display label
//
// EPUB2: finds the manifest item with media-type application/x-dtbncx+xml.
// EPUB3: finds the manifest item with properties containing "nav".
// If neither is found, both maps are empty.
func readNCXIndex(r *zip.Reader, pkg xmlPackage, opfDir string, idx map[string]*zip.File) (titleToPath map[string]string, pathToTitle map[string]string, err error) {
	titleToPath = make(map[string]string)
	pathToTitle = make(map[string]string)

	// Find NCX or nav document in manifest.
	var ncxHref, navHref string
	for _, item := range pkg.Manifest.Items {
		if item.MediaType == "application/x-dtbncx+xml" {
			ncxHref = path.Join(opfDir, item.Href)
		}
		if strings.Contains(item.Properties, "nav") {
			navHref = path.Join(opfDir, item.Href)
		}
	}

	// Prefer NCX for EPUB2; use nav document for EPUB3 (no NCX).
	if ncxHref != "" {
		f, ok := idx[ncxHref]
		if !ok {
			// Try root-relative lookup.
			f, ok = idx[path.Base(ncxHref)]
		}
		if ok {
			data, readErr := readZipEntry(f)
			if readErr == nil {
				var ncx xmlNCX
				if xmlErr := xml.Unmarshal(data, &ncx); xmlErr == nil {
					ncxDir := path.Dir(ncxHref)
					for _, pt := range flattenNavPoints(ncx.NavMap.NavPoints) {
						label := strings.TrimSpace(pt.NavLabel.Text)
						src := pt.Content.Src
						if label == "" || src == "" {
							continue
						}
						// Strip fragment identifier.
						if i := strings.Index(src, "#"); i >= 0 {
							src = src[:i]
						}
						fullPath := path.Join(ncxDir, src)
						norm := normalizeTitle(label)
						if _, exists := titleToPath[norm]; !exists {
							titleToPath[norm] = fullPath
						}
						if _, exists := pathToTitle[fullPath]; !exists {
							pathToTitle[fullPath] = label
						}
					}
					return titleToPath, pathToTitle, nil
				}
			}
		}
	}

	if navHref != "" {
		f, ok := idx[navHref]
		if ok {
			data, readErr := readZipEntry(f)
			if readErr == nil {
				navDir := path.Dir(navHref)
				parseEPUB3NavData(data, navDir, titleToPath, pathToTitle)
			}
		}
	}

	return titleToPath, pathToTitle, nil
}

var (
	reNavAnchor = regexp.MustCompile(`(?i)<a[^>]+href="([^"#][^"]*)"[^>]*>([^<]+)</a>`)
)

// parseEPUB3NavData extracts href->title pairs from an EPUB3 nav document
// using regex. XML namespace handling makes xml.Unmarshal unreliable for
// nav documents in practice.
func parseEPUB3NavData(data []byte, navDir string, titleToPath, pathToTitle map[string]string) {
	matches := reNavAnchor.FindAllSubmatch(data, -1)
	for _, m := range matches {
		href := string(m[1])
		label := strings.TrimSpace(string(m[2]))
		if href == "" || label == "" {
			continue
		}
		if idx := strings.Index(href, "#"); idx >= 0 {
			href = href[:idx]
		}
		fullPath := path.Join(navDir, href)
		norm := normalizeTitle(label)
		if _, exists := titleToPath[norm]; !exists {
			titleToPath[norm] = fullPath
		}
		if _, exists := pathToTitle[fullPath]; !exists {
			pathToTitle[fullPath] = label
		}
	}
}

// ---------------------------------------------------------------------------
// resolveSpinePaths
// ---------------------------------------------------------------------------

// resolveSpinePaths reads the OPF spine from r and returns the full
// zip-internal paths of every spine item in order. Used when a BookConfig
// has UseSpine set to true. The returned slice is 0-indexed; chapter num N
// in a config maps to index N-1. Non-XHTML items (images, SVG maps) are
// included in the index to preserve position -- a warning is printed if the
// build later tries to use one as a chapter source.
func resolveSpinePaths(r *zip.Reader) ([]string, error) {
	opfDir, pkg, _, err := readOPF(r)
	if err != nil {
		return nil, err
	}

	hrefByID := make(map[string]string, len(pkg.Manifest.Items))
	mediaByID := make(map[string]string, len(pkg.Manifest.Items))
	for _, item := range pkg.Manifest.Items {
		hrefByID[item.ID] = path.Join(opfDir, item.Href)
		mediaByID[item.ID] = item.MediaType
	}

	var paths []string
	for _, ref := range pkg.Spine.Items {
		href, ok := hrefByID[ref.IDRef]
		if !ok {
			continue
		}
		mt := mediaByID[ref.IDRef]
		if mt != "" && mt != "application/xhtml+xml" && mt != "text/html" {
			// Non-XHTML spine item (SVG map, image page, etc.). Include a
			// placeholder so indices remain correct, but the validation step
			// will warn if this index is referenced by a chapter entry.
			paths = append(paths, "NON_XHTML:"+href)
			continue
		}
		paths = append(paths, href)
	}
	return paths, nil
}

// ---------------------------------------------------------------------------
// autoDetectBookConfig
// ---------------------------------------------------------------------------

// autoDetectBookConfig inspects the OPF of an open epub and fills in any
// zero-value fields of existing that can be derived from the manifest
// metadata. Fields already set in existing are left unchanged.
//
// Detected fields: css_src, css_dest, cover_src, image_src_prefix,
// image_dest, and NCXIndex (populated from the NCX or EPUB3 nav document).
//
// This function does NOT set UseSpine. Chapter path resolution (UseSpine,
// SpineOffset, ChapterPaths) is a separate concern -- auto_detect deals only
// with asset paths and the title index. For unknown epub editions where there
// is no built-in path function, set use_spine: true alongside auto_detect to
// also resolve chapter paths from the spine.
func autoDetectBookConfig(bookID string, r *zip.Reader, existing BookConfig) (BookConfig, error) {
	opfDir, pkg, idx, err := readOPF(r)
	if err != nil {
		return existing, err
	}

	hrefByID := make(map[string]string, len(pkg.Manifest.Items))
	for _, item := range pkg.Manifest.Items {
		hrefByID[item.ID] = path.Join(opfDir, item.Href)
	}

	bk := existing

	// CSS: first manifest item with media-type text/css.
	if bk.CSSSource == "" {
		for _, item := range pkg.Manifest.Items {
			if item.MediaType == "text/css" {
				bk.CSSSource = path.Join(opfDir, item.Href)
				break
			}
		}
	}
	if bk.CSSDest == "" {
		bk.CSSDest = strings.ToLower(bookID) + ".css"
	}

	// Cover: OPF metadata <meta name="cover" content="item-id"/>.
	// Falls back to scanning manifest items whose filename contains "cover"
	// or "cvi" if the ID lookup fails (some editions use a different ID
	// in the meta tag than in the manifest).
	if bk.CoverSrc == "" {
		var coverID string
		for _, m := range pkg.Metadata.Meta {
			if strings.EqualFold(m.Name, "cover") && m.Content != "" {
				coverID = m.Content
				break
			}
		}
		if coverID != "" {
			if href, ok := hrefByID[coverID]; ok {
				bk.CoverSrc = href
			}
		}
		if bk.CoverSrc == "" {
			for _, item := range pkg.Manifest.Items {
				if !strings.HasPrefix(item.MediaType, "image/") {
					continue
				}
				base := strings.ToLower(path.Base(item.Href))
				if strings.Contains(base, "cover") || strings.Contains(base, "cvi") {
					bk.CoverSrc = path.Join(opfDir, item.Href)
					break
				}
			}
		}
	}

	// Image prefix: longest common directory prefix among image manifest
	// items, excluding root-level files and the identified cover image.
	if bk.ImageSrcPrefix == "" {
		var imageDirs []string
		for _, item := range pkg.Manifest.Items {
			if !strings.HasPrefix(item.MediaType, "image/") {
				continue
			}
			full := path.Join(opfDir, item.Href)
			if full == bk.CoverSrc {
				continue
			}
			dir := path.Dir(full)
			if dir == "." || dir == "" || dir == opfDir {
				continue
			}
			imageDirs = append(imageDirs, dir+"/")
		}
		if len(imageDirs) > 0 {
			bk.ImageSrcPrefix = commonPrefix(imageDirs)
		}
	}
	if bk.ImageDest == "" {
		bk.ImageDest = strings.ToLower(bookID)
	}

	// NCX / nav index for title-based chapter lookup.
	titleToPath, _, ncxErr := readNCXIndex(r, pkg, opfDir, idx)
	if ncxErr == nil && len(titleToPath) > 0 {
		bk.NCXIndex = titleToPath
	}

	return bk, nil
}

// commonPrefix returns the longest common prefix of a slice of strings.
func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	prefix := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

// ---------------------------------------------------------------------------
// runScan
// ---------------------------------------------------------------------------

// runScan prints the spine of an epub: spine index (1-based config num),
// filename, NCX/nav title if found, and a short first-text snippet. This
// lets users figure out which num values to use in a config, and whether
// a spine_offset would simplify authoring.
func runScan(epubPath string) error {
	r, err := zip.OpenReader(epubPath)
	if err != nil {
		return fmt.Errorf("opening epub: %w", err)
	}
	defer r.Close()

	opfDir, pkg, idx, err := readOPF(&r.Reader)
	if err != nil {
		return err
	}

	hrefByID := make(map[string]string, len(pkg.Manifest.Items))
	for _, item := range pkg.Manifest.Items {
		hrefByID[item.ID] = path.Join(opfDir, item.Href)
	}

	// Build path->title map from NCX or nav document.
	_, pathToTitle, _ := readNCXIndex(&r.Reader, pkg, opfDir, idx)

	fmt.Printf("Epub:    %s\n", epubPath)
	fmt.Printf("OPF dir: %s  (%d spine items)\n", opfDir, len(pkg.Spine.Items))
	if len(pathToTitle) > 0 {
		fmt.Printf("NCX/nav: %d entries indexed\n", len(pathToTitle))
	} else {
		fmt.Printf("NCX/nav: not found\n")
	}

	// Find the first spine entry that has an NCX label -- that index is the
	// spine_offset for this epub if chapter nums use narrative numbering.
	firstLabelledIdx := -1
	for i, ref := range pkg.Spine.Items {
		href := hrefByID[ref.IDRef]
		if _, ok := pathToTitle[href]; ok {
			firstLabelledIdx = i
			break
		}
	}
	if firstLabelledIdx >= 0 {
		fmt.Printf("\nHint: first NCX-labelled spine entry is at index %d (config num %d).\n",
			firstLabelledIdx, firstLabelledIdx+1)
		fmt.Printf("      With auto_detect, chapter titles are matched directly and no offset is needed.\n")
		fmt.Printf("      With use_spine only, set spine_offset: %d to use narrative chapter numbers.\n",
			firstLabelledIdx)
	}

	fmt.Printf("\n%-5s  %-38s  %-30s  %s\n", "Num", "File", "NCX Title", "First text")
	fmt.Println(strings.Repeat("-", 120))

	for i, ref := range pkg.Spine.Items {
		num := i + 1
		href, ok := hrefByID[ref.IDRef]
		if !ok {
			fmt.Printf("%-5d  %-38s  %-30s  (manifest item not found: %s)\n", num, "?", "", ref.IDRef)
			continue
		}

		f, ok := idx[href]
		if !ok {
			f, ok = idx[path.Base(href)]
		}

		base := path.Base(href)
		ncxLabel := pathToTitle[href]

		if !ok {
			fmt.Printf("%-5d  %-38s  %-30s  (file missing from zip)\n", num, base, ncxLabel)
			continue
		}

		raw, _ := readZipEntry(f)
		snippet := firstTextSnippet(raw, 50)
		fmt.Printf("%-5d  %-38s  %-30s  %s\n", num, base, ncxLabel, snippet)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Text extraction helper
// ---------------------------------------------------------------------------

var reTagStrip = regexp.MustCompile(`<[^>]+>`)

// firstTextSnippet strips HTML tags from src and returns the first n
// printable characters, suitable for a one-line preview.
func firstTextSnippet(src []byte, n int) string {
	text := reTagStrip.ReplaceAllString(string(src), " ")
	fields := strings.Fields(text)
	joined := strings.Join(fields, " ")
	runes := []rune(joined)
	if len(runes) > n {
		return string(runes[:n]) + "..."
	}
	return joined
}

// ---------------------------------------------------------------------------
// scan --init: generate a skeleton JSON config from an epub's spine
// ---------------------------------------------------------------------------

// runScanInit reads an epub and writes a skeleton JSON config to outPath. Each
// spine entry becomes a chapter in the config, with the title taken from the
// NCX label if available, otherwise from a text snippet of the page content.
// The output is a starting point for authoring a custom config; the user
// reorders, removes, or relabels entries as needed.
func runScanInit(epubPath, outPath, bookID string) error {
	r, err := zip.OpenReader(epubPath)
	if err != nil {
		return fmt.Errorf("opening epub: %w", err)
	}
	defer r.Close()

	opfDir, pkg, idx, err := readOPF(&r.Reader)
	if err != nil {
		return err
	}

	hrefByID := make(map[string]string, len(pkg.Manifest.Items))
	mediaByID := make(map[string]string, len(pkg.Manifest.Items))
	for _, item := range pkg.Manifest.Items {
		hrefByID[item.ID] = path.Join(opfDir, item.Href)
		mediaByID[item.ID] = item.MediaType
	}

	_, pathToTitle, _ := readNCXIndex(&r.Reader, pkg, opfDir, idx)

	var chapters []ChapterEntry
	num := 0
	for _, ref := range pkg.Spine.Items {
		href, ok := hrefByID[ref.IDRef]
		if !ok {
			continue
		}
		mt := mediaByID[ref.IDRef]
		if mt != "" && mt != "application/xhtml+xml" && mt != "text/html" {
			continue // Skip non-XHTML items.
		}
		num++

		title := pathToTitle[href]
		if title == "" {
			// Try to derive a title from the page content.
			if f, ok := idx[href]; ok {
				if raw, err := readZipEntry(f); err == nil {
					title = firstTextSnippet(raw, 40)
				}
			}
		}
		if title == "" {
			title = fmt.Sprintf("Chapter %d", num)
		}

		chapters = append(chapters, ChapterEntry{
			Title: title,
			Book:  bookID,
			Num:   num,
		})
	}

	cfg := Config{
		Name: "My Reading Order",
		Books: map[string]BookConfig{
			bookID: {
				AutoDetect: true,
				UseSpine:   true,
			},
		},
		FrontMatter: []FrontMatterEntry{},
		Chapters:    chapters,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding JSON: %w", err)
	}

	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}

	fmt.Printf("Wrote skeleton config with %d chapters to %s\n", len(chapters), outPath)
	fmt.Printf("Book ID: %s (auto_detect + use_spine enabled)\n", bookID)
	fmt.Printf("Edit the file to reorder, remove, or relabel chapters as needed.\n")
	return nil
}
