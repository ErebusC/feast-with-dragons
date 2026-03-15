package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Audio file collection
// ---------------------------------------------------------------------------

// collectAudioFiles returns a sorted list of audio file paths. p may be
// a directory (all audio files within it are returned) or a single file path.
func collectAudioFiles(p string) ([]string, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		return []string{abs}, nil
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isAudioExt(strings.ToLower(filepath.Ext(e.Name()))) {
			abs, err := filepath.Abs(filepath.Join(p, e.Name()))
			if err != nil {
				return nil, err
			}
			files = append(files, abs)
		}
	}
	sort.Strings(files)
	return files, nil
}

// ---------------------------------------------------------------------------
// Logical chapter assembly
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Audio builder
// ---------------------------------------------------------------------------

// runAudio builds a spliced M4B. sources maps each book ID to one or more
// file or directory paths containing its audio.
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
	fmt.Fprintf(metaFile, ";FFMETADATA1\ntitle=%s\nartist=%s\n\n",
		escapeFFMeta(cfg.Name), escapeFFMeta(author))

	logf("Extracting chapters...\n")
	extractStart := time.Now()
	var offsetMs int64
	for i, ch := range cfg.Chapters {
		bookID, num := ch.Book, ch.Num
		if ch.IsCombined() {
			bookID, num = ch.Parts[0].Book, ch.Parts[0].Num
		}
		lc, _ := getSegment(bookID, num, ch.Title)

		// Print progress before extraction so the user sees activity
		// immediately, not after a potentially slow segment completes.
		elapsed := time.Since(extractStart).Truncate(time.Second)
		logf("  [%03d/%d] %s (%.1fs, %d segment(s)) [%s elapsed]\n",
			i+1, len(cfg.Chapters), ch.Title, lc.DurSec(), len(lc.Segments), elapsed)

		for j, seg := range lc.Segments {
			segPath := filepath.Join(tmpDir, fmt.Sprintf("seg_%04d_%02d.m4a", i, j))
			// Stream-copy for speed. This preserves original timestamps
			// which can cause cosmetic PTS/DTS warnings during the concat
			// pass, but the output audio is correct and plays without
			// issues in all tested players.
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
			// Use the bare filename in the concat list. The concat file
			// and all segment files are in the same temp directory, and
			// ffmpeg resolves paths relative to the concat file's location.
			segName := fmt.Sprintf("seg_%04d_%02d.m4a", i, j)
			escaped := strings.ReplaceAll(segName, "'", `\'`)
			fmt.Fprintf(concatFile, "file '%s'\n", escaped)
		}

		durMs := int64(lc.DurSec() * 1000)
		start := offsetMs
		offsetMs += durMs
		fmt.Fprintf(metaFile, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n\n",
			start, offsetMs, escapeFFMeta(ch.Title))
	}
	if err := concatFile.Close(); err != nil {
		return fmt.Errorf("writing concat list: %w", err)
	}
	if err := metaFile.Close(); err != nil {
		return fmt.Errorf("writing chapter metadata: %w", err)
	}

	logf("\n  Extraction complete (%s).\n", time.Since(extractStart).Truncate(time.Second))
	logf("  Total audio duration: %s\n", formatDuration(float64(offsetMs)/1000))
	logf("\nConcatenating %d segments and writing chapter metadata...\n", len(cfg.Chapters))
	logf("  This includes a faststart pass that rewrites the file for faster seeking.\n")
	logf("  The tool will exit once this completes.\n")

	concatStart := time.Now()
	err = runCommand("ffmpeg",
		"-y",
		"-loglevel", "error",
		"-f", "concat",
		"-safe", "0",
		"-i", concatPath,
		"-i", metaPath,
		"-map", "0:a",
		"-map_metadata", "1",
		"-map_chapters", "1",
		"-c", "copy",
		"-movflags", "+faststart",
		outputPath,
	)
	if err != nil {
		return err
	}

	info, _ := os.Stat(outputPath)
	logf("\nDone. Output: %s (%.1f MB, %s)\n",
		outputPath,
		float64(info.Size())/1_048_576,
		time.Since(concatStart).Truncate(time.Second))
	return nil
}

// formatDuration converts seconds to a human-readable "Xh Ym Zs" string.
func formatDuration(secs float64) string {
	total := int(secs)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
