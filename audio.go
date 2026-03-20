package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
		// Show the first few and last few audio chapter titles so the user
		// can verify the mapping looks correct before extraction begins.
		preview := 5
		if len(segs) <= preview*2 {
			for si, seg := range segs {
				logf("    [%d] %s (%.0fs at %.1fs)\n", si+1, seg.Title, seg.DurSec(), seg.Segments[0].StartSec)
			}
		} else {
			for si := 0; si < preview; si++ {
				logf("    [%d] %s (%.0fs at %.1fs)\n", si+1, segs[si].Title, segs[si].DurSec(), segs[si].Segments[0].StartSec)
			}
			logf("    ... (%d more) ...\n", len(segs)-preview*2)
			for si := len(segs) - preview; si < len(segs); si++ {
				logf("    [%d] %s (%.0fs at %.1fs)\n", si+1, segs[si].Title, segs[si].DurSec(), segs[si].Segments[0].StartSec)
			}
		}
		segsPerBook[id] = segs
	}

	// Probe audio format from source files to determine encoding parameters.
	// When source books have different codecs, sample rates, or channel
	// counts, stream-copy produces a file where only the first segment's
	// format is honoured by the decoder. Re-encoding all segments to a
	// consistent format avoids this.
	var encArgs []string
	{
		var maxRate, maxCh, maxBR int
		for _, id := range orderedIDs {
			segs := segsPerBook[id]
			if len(segs) == 0 {
				continue
			}
			// Probe the first segment's source file.
			af, err := probeAudioFormat(segs[0].Segments[0].File)
			if err != nil {
				logf("  WARNING: could not probe %s audio format: %v\n", id, err)
				continue
			}
			logf("  %s format: %s %dHz %dch %dkbps\n",
				id, af.Codec, af.SampleRate, af.Channels, af.BitRate/1000)
			if af.SampleRate > maxRate {
				maxRate = af.SampleRate
			}
			if af.Channels > maxCh {
				maxCh = af.Channels
			}
			if af.BitRate > maxBR {
				maxBR = af.BitRate
			}
		}
		// Fall back to sensible defaults if probing failed.
		if maxRate == 0 {
			maxRate = 44100
		}
		if maxCh == 0 {
			maxCh = 2
		}
		if maxBR == 0 {
			maxBR = 128000
		}
		// Round bitrate to nearest common value.
		brK := maxBR / 1000
		if brK < 48 {
			brK = 48
		}
		logf("  Output format: aac %dHz %dch %dkbps\n", maxRate, maxCh, brK)

		encArgs = []string{
			"-c:a", "aac",
			"-b:a", fmt.Sprintf("%dk", brK),
			"-ar", fmt.Sprintf("%d", maxRate),
			"-ac", fmt.Sprintf("%d", maxCh),
		}
	}

	lookupSegment := func(bookID string, num int) (LogicalChapter, error) {
		segs, ok := segsPerBook[bookID]
		if !ok {
			return LogicalChapter{}, fmt.Errorf("no audio loaded for book %q", bookID)
		}
		idx := num - 1
		if idx < 0 || idx >= len(segs) {
			return LogicalChapter{}, fmt.Errorf("%s chapter %d (index %d) out of range (have %d segments)",
				bookID, num, idx, len(segs))
		}
		return segs[idx], nil
	}

	logf("Validating chapter mapping...\n")
	for i, ch := range cfg.Chapters {
		bookID := ch.Book
		audioNum := ch.AudioEffectiveNum()
		if ch.IsCombined() {
			bookID = ch.Parts[0].Book
			audioNum = ch.Parts[0].Num
		}
		lc, err := lookupSegment(bookID, audioNum)
		if err != nil {
			return err
		}
		// Title mismatch note -- only logged here, not during extraction.
		expected := povName(ch.Title)
		if !strings.EqualFold(lc.Title, expected) &&
			!strings.Contains(strings.ToLower(lc.Title), strings.ToLower(expected)) &&
			!strings.Contains(strings.ToLower(expected), strings.ToLower(lc.Title)) {
			logf("  NOTE: %s ch%d -- config title %q, audio title %q\n", bookID, audioNum, expected, lc.Title)
		}
		seg := lc.Segments[0]
		startSec := seg.StartSec
		endSec := seg.StartSec + lc.DurSec()
		suffix := ""
		if ch.AudioStart != nil {
			startSec = *ch.AudioStart
			suffix += fmt.Sprintf(" [audio_start=%.1f]", startSec)
		}
		if ch.AudioEnd != nil {
			endSec = *ch.AudioEnd
			suffix += fmt.Sprintf(" [audio_end=%.1f]", endSec)
		}
		if ch.AudioNum > 0 {
			suffix += fmt.Sprintf(" [audio_num=%d]", ch.AudioNum)
		}
		logf("  [%03d] %s -> %s seg %d %q (%.1fs-%.1fs)%s\n",
			i+1, ch.Title, bookID, audioNum, lc.Title,
			startSec, endSec, suffix)
		dur := endSec - startSec
		if dur < 60 {
			logf("  WARNING: segment is only %.0fs -- this audiobook edition may have combined it with the preceding chapter\n", dur)
		}
	}

	tmpDir, err := os.MkdirTemp(filepath.Dir(outputPath), "feast-with-dragons-audio-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// Build the list of extraction jobs, the concat file, and the metadata
	// file up front. All filenames and durations are known before extraction
	// starts, so these can be written first and then extractions run in
	// parallel without coordination.

	type extractJob struct {
		segPath  string
		file     string
		startSec float64
		durSec   float64
		label    string // for error messages
	}

	var jobs []extractJob
	var concatLines []string
	var offsetMs int64

	author := cfg.Author
	if author == "" {
		author = "Unknown"
	}

	var metaBuf strings.Builder
	fmt.Fprintf(&metaBuf, ";FFMETADATA1\ntitle=%s\nartist=%s\n\n",
		escapeFFMeta(cfg.Name), escapeFFMeta(author))

	for i, ch := range cfg.Chapters {
		bookID := ch.Book
		audioNum := ch.AudioEffectiveNum()
		if ch.IsCombined() {
			bookID = ch.Parts[0].Book
			audioNum = ch.Parts[0].Num
		}
		lc, _ := lookupSegment(bookID, audioNum)

		// When audio_start or audio_end are set, the chapter is extracted
		// from explicit timestamps rather than the segment's metadata
		// boundaries. This handles audiobook editions where two book
		// chapters are merged into one audio track.
		if ch.AudioStart != nil || ch.AudioEnd != nil {
			seg := lc.Segments[0]
			startSec := seg.StartSec
			endSec := seg.EndSec
			if ch.AudioStart != nil {
				startSec = *ch.AudioStart
			}
			if ch.AudioEnd != nil {
				endSec = *ch.AudioEnd
			}
			dur := endSec - startSec
			segName := fmt.Sprintf("seg_%04d_00.m4a", i)
			segPath := filepath.Join(tmpDir, segName)
			jobs = append(jobs, extractJob{
				segPath:  segPath,
				file:     seg.File,
				startSec: startSec,
				durSec:   dur,
				label:    fmt.Sprintf("%s ch%d (override)", bookID, audioNum),
			})
			escaped := strings.ReplaceAll(segName, "'", `\'`)
			concatLines = append(concatLines, fmt.Sprintf("file '%s'", escaped))

			durMs := int64(dur * 1000)
			start := offsetMs
			offsetMs += durMs
			fmt.Fprintf(&metaBuf, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n\n",
				start, offsetMs, escapeFFMeta(ch.Title))
			continue
		}

		for j, seg := range lc.Segments {
			segName := fmt.Sprintf("seg_%04d_%02d.m4a", i, j)
			segPath := filepath.Join(tmpDir, segName)
			jobs = append(jobs, extractJob{
				segPath:  segPath,
				file:     seg.File,
				startSec: seg.StartSec,
				durSec:   seg.DurSec(),
				label:    fmt.Sprintf("%s ch%d seg%d", bookID, audioNum, j),
			})
			escaped := strings.ReplaceAll(segName, "'", `\'`)
			concatLines = append(concatLines, fmt.Sprintf("file '%s'", escaped))
		}

		durMs := int64(lc.DurSec() * 1000)
		start := offsetMs
		offsetMs += durMs
		fmt.Fprintf(&metaBuf, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n\n",
			start, offsetMs, escapeFFMeta(ch.Title))
	}

	// Write concat and metadata files.
	concatPath := filepath.Join(tmpDir, "concat.txt")
	metaPath := filepath.Join(tmpDir, "chapters.txt")

	if err := os.WriteFile(concatPath, []byte(strings.Join(concatLines, "\n")+"\n"), 0644); err != nil {
		return fmt.Errorf("writing concat list: %w", err)
	}
	if err := os.WriteFile(metaPath, []byte(metaBuf.String()), 0644); err != nil {
		return fmt.Errorf("writing chapter metadata: %w", err)
	}

	// Extract segments in parallel.
	workers := audioConcurrency
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}

	logf("Extracting and re-encoding %d segments using %d workers...\n", len(jobs), workers)
	logf("  Re-encoding normalises audio format across source books for clean concatenation.\n")
	extractStart := time.Now()

	var completed int64
	total := int64(len(jobs))
	var firstErr error
	var errOnce sync.Once

	jobCh := make(chan extractJob, len(jobs))
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobCh {
				// Re-encode each segment to a consistent format so the
				// concat demuxer produces a valid output file regardless
				// of source codec differences between books.
				args := []string{
					"-y",
					"-i", job.file,
					"-ss", fmt.Sprintf("%f", job.startSec),
					"-t", fmt.Sprintf("%f", job.durSec),
					"-map", "0:a",
				}
				args = append(args, encArgs...)
				args = append(args, job.segPath)

				err := runCommandSilent("ffmpeg", args...)
				if err != nil {
					errOnce.Do(func() {
						firstErr = fmt.Errorf("extracting %s: %w", job.label, err)
					})
					return
				}
				done := atomic.AddInt64(&completed, 1)
				if done%10 == 0 || done == total {
					elapsed := time.Since(extractStart).Truncate(time.Second)
					logf("  %d/%d segments extracted [%s elapsed]\n", done, total, elapsed)
				}
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}

	logf("  Extraction complete (%s).\n", time.Since(extractStart).Truncate(time.Second))
	logf("  Total audio duration: %s\n", formatDuration(float64(offsetMs)/1000))
	logf("\nConcatenating %d segments and writing chapter metadata...\n", len(jobs))
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
