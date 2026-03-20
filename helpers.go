package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Package-level flags and logging
// ---------------------------------------------------------------------------

var (
	quietMode        bool
	forceMode        bool
	audioConcurrency int
)

// logf prints a message to stdout unless quiet mode is active.
func logf(format string, args ...any) {
	if !quietMode {
		fmt.Printf(format, args...)
	}
}

// addCommonFlags registers -quiet and -force on a FlagSet.
func addCommonFlags(fs *flag.FlagSet) {
	fs.BoolVar(&quietMode, "quiet", false, "Suppress progress output")
	fs.BoolVar(&forceMode, "force", false, "Overwrite existing output file")
}

// checkOutputExists exits with an error if the output file exists and --force
// is not set. With --force it removes the existing file.
func checkOutputExists(p string) {
	if _, err := os.Stat(p); err != nil {
		return
	}
	if forceMode {
		if err := os.Remove(p); err != nil {
			fmt.Fprintf(os.Stderr, "Cannot remove existing output file: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "Output file already exists: %s\nRemove it, use -out to specify a different path, or pass -force to overwrite.\n", p)
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Shared audio extension set
// ---------------------------------------------------------------------------

var audioExts = map[string]bool{
	".mp3":  true,
	".m4b":  true,
	".m4a":  true,
	".flac": true,
	".ogg":  true,
	".opus": true,
}

// isAudioExt returns true if ext (including the leading dot, lowercase) is a
// recognised audio format.
func isAudioExt(ext string) bool { return audioExts[ext] }

// ---------------------------------------------------------------------------
// Shared ID and path helpers
// ---------------------------------------------------------------------------

var idReplacer = strings.NewReplacer(".", "_", "-", "_", " ", "_")

func sanitiseID(s string) string { return idReplacer.Replace(s) }

func imageMIME(name string) string {
	switch strings.ToLower(path.Ext(name)) {
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

func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// ---------------------------------------------------------------------------
// Config helpers
// ---------------------------------------------------------------------------

// UsedBookIDs returns the sorted list and set of book IDs referenced by any
// chapter or front matter entry in the config.
func (cfg *Config) UsedBookIDs() ([]string, map[string]bool) {
	set := map[string]bool{}
	for _, ch := range cfg.Chapters {
		if ch.IsCombined() {
			for _, p := range ch.Parts {
				set[p.Book] = true
			}
		} else {
			set[ch.Book] = true
		}
	}
	for _, fm := range cfg.FrontMatter {
		set[fm.Book] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, set
}

// ---------------------------------------------------------------------------
// Source epub preparation (shared by ebook and validate)
// ---------------------------------------------------------------------------

// prepareBooks opens source epubs, runs auto-detect for books that request it,
// and resolves spine paths for books with use_spine set. It returns updated
// book configs, zip indexes for each opened source, and any error. The caller
// is responsible for closing the returned ReadClosers.
func prepareBooks(
	sources map[string]string,
	books map[string]BookConfig,
) (
	map[string]BookConfig,
	map[string]*zip.ReadCloser,
	map[string]map[string]*zip.File,
	error,
) {
	zipReaders := make(map[string]*zip.ReadCloser, len(sources))
	zipIndexes := make(map[string]map[string]*zip.File, len(sources))
	for id, p := range sources {
		logf("Opening %s: %s\n", id, p)
		rc, err := zip.OpenReader(p)
		if err != nil {
			// Close any already-opened readers.
			for _, prev := range zipReaders {
				prev.Close()
			}
			return nil, nil, nil, fmt.Errorf("opening %s epub: %w", id, err)
		}
		zipReaders[id] = rc
		zipIndexes[id] = zipIndex(&rc.Reader)
	}

	// Auto-detect: inspect OPF manifest for asset paths and NCX index.
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
			return nil, nil, nil, fmt.Errorf("auto-detecting config for %s: %w", id, err)
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

	// UseSpine: read OPF spine and populate ChapterPaths.
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
			return nil, nil, nil, fmt.Errorf("resolving spine for %s: %w", id, err)
		}
		logf("  %s: %d spine items resolved from OPF\n", id, len(paths))
		if bk.SpineOffset > 0 {
			logf("  %s: spine_offset=%d applied (narrative ch N -> spine entry N+%d)\n",
				id, bk.SpineOffset, bk.SpineOffset)
		}
		bk.ChapterPaths = paths
		books[id] = bk
	}

	return books, zipReaders, zipIndexes, nil
}

// ---------------------------------------------------------------------------
// Source auto-detection (shared by ebook, audio, validate CLI blocks)
// ---------------------------------------------------------------------------

// resolveDefaultSources adds AFFC and ADWD entries to sources if they are
// missing, by searching cwd for files matching the expected keywords.
func resolveDefaultSources(sources map[string]string, cwd, ext string) {
	if sources["AFFC"] == "" {
		if p := autoDetect(cwd, ext, "feast", "crows"); p != "" {
			sources["AFFC"] = p
		}
	}
	if sources["ADWD"] == "" {
		if p := autoDetect(cwd, ext, "dance", "dragons"); p != "" {
			sources["ADWD"] = p
		}
	}
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
// contain all of keywords, sorted by filename.
func autoDetectAudioFiles(dir string, keywords ...string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []string
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if !isAudioExt(filepath.Ext(name)) {
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

// ---------------------------------------------------------------------------
// Flag parsing helpers
// ---------------------------------------------------------------------------

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

func (r *repeatable) String() string     { return strings.Join(*r, ", ") }
func (r *repeatable) Set(v string) error { *r = append(*r, v); return nil }

func binaryName() string { return filepath.Base(os.Args[0]) }

// ---------------------------------------------------------------------------
// Interactive prompt helpers
// ---------------------------------------------------------------------------

// readYesNo reads one line from stdin and returns true if the response is
// empty (user pressed Enter), "y", or "yes". Any other input returns false.
func readYesNo() bool {
	var resp string
	fmt.Fscan(os.Stdin, &resp)
	r := strings.ToLower(strings.TrimSpace(resp))
	return r == "" || r == "y" || r == "yes"
}

// sortedKeys returns the keys of m sorted alphabetically.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// FFmpeg metadata escaping
// ---------------------------------------------------------------------------

// escapeFFMeta escapes a string for use as a value in FFMETADATA1 format.
// Characters =, ;, #, \, and newline must be backslash-escaped.
func escapeFFMeta(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`=`, `\=`,
		`;`, `\;`,
		`#`, `\#`,
		"\n", `\n`,
	)
	return r.Replace(s)
}
