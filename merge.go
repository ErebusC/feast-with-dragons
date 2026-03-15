package main

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// Audio merge
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
	fmt.Fprintf(metaFile, ";FFMETADATA1\ntitle=%s\nartist=%s\n\n",
		escapeFFMeta(title), escapeFFMeta(author))

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
				start, offsetMs, escapeFFMeta(seg.Title))
		}
	}
	if err := concatFile.Close(); err != nil {
		return fmt.Errorf("writing concat list: %w", err)
	}
	if err := metaFile.Close(); err != nil {
		return fmt.Errorf("writing chapter metadata: %w", err)
	}

	logf("\nConcatenating and writing chapter metadata...\n")
	logf("  This includes a faststart pass that rewrites the file for faster seeking.\n")
	logf("  The tool will exit once this completes.\n")
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
	logf("\nDone. Output: %s (%.1f MB)\n", outputPath, float64(info.Size())/1_048_576)
	return nil
}

// ---------------------------------------------------------------------------
// Epub merge
// ---------------------------------------------------------------------------

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

	cw, err := startEpubZip(out)
	if err != nil {
		return err
	}

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

			// Parse OPF using the shared XML types from scan.go.
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

			var pkg xmlPackage
			if err := xml.Unmarshal(opfData, &pkg); err != nil {
				return fmt.Errorf("parsing OPF from %s: %w", filepath.Base(p), err)
			}

			itemHrefs := map[string]string{}
			for _, item := range pkg.Manifest.Items {
				itemHrefs[item.ID] = item.Href
			}

			opfDir := path.Dir(opfPath)
			for _, ref := range pkg.Spine.Items {
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

			logf("  %s: %d spine items\n", filepath.Base(p), len(pkg.Spine.Items))
			return nil
		}(); err != nil {
			return err
		}
	}

	bookID := sanitiseID(strings.ToLower(title))
	cw.write("OEBPS/content.opf",
		[]byte(buildOPF(bookID, title, author, manifestItems, spineItems, false)))
	cw.write("OEBPS/toc.ncx",
		[]byte(buildNCX(bookID, title, navPoints, nil)))

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
