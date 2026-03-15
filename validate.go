package main

import (
	"fmt"
	"sort"
	"strings"
)

// runValidate checks a config against the provided source epubs without
// producing any output. It reports missing chapters, duplicate entries, and
// other problems.
func runValidate(sources map[string]string, cfg *Config) error {
	books := cfg.effectiveBooks()
	_, usedSet := cfg.UsedBookIDs()

	var problems []string

	for id := range usedSet {
		if sources[id] == "" {
			problems = append(problems, fmt.Sprintf("  no source epub provided for book ID %q", id))
		}
	}

	books, zipReaders, zipIndexes, err := prepareBooks(sources, books)
	if err != nil {
		problems = append(problems, fmt.Sprintf("  %v", err))
	}
	for _, rc := range zipReaders {
		defer rc.Close()
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
				return
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
	ids := make([]string, 0, len(usedSet))
	for id := range usedSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fmt.Printf("Books:    %d referenced (%s)\n", len(usedSet), strings.Join(ids, ", "))

	if len(problems) == 0 {
		fmt.Printf("\nAll %d chapters validated successfully.\n", total)
		return nil
	}
	return fmt.Errorf("%d problem(s) found:\n%s", len(problems), strings.Join(problems, "\n"))
}
