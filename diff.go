package main

import (
	"fmt"
	"strings"
)

// chapterSig returns a short canonical identifier for a chapter entry,
// combining book ID and chapter number(s) into a string like "AFFC:4" or
// "AFFC:6+ADWD:8" for combined chapters.
func chapterSig(e ChapterEntry) string {
	if e.IsCombined() {
		var parts []string
		for _, p := range e.Parts {
			parts = append(parts, fmt.Sprintf("%s:%d", p.Book, p.Num))
		}
		return strings.Join(parts, "+")
	}
	return fmt.Sprintf("%s:%d", e.Book, e.Num)
}

// runDiff compares two configs and prints differences in chapter ordering,
// additions, and removals.
func runDiff(leftName, rightName string, left, right *Config) {
	fmt.Printf("Comparing %q (%d chapters) vs %q (%d chapters)\n\n",
		left.Name, len(left.Chapters), right.Name, len(right.Chapters))

	// Build maps of sig -> positions for each config.
	type entry struct {
		Title string
		Pos   int
	}
	leftMap := make(map[string]entry, len(left.Chapters))
	rightMap := make(map[string]entry, len(right.Chapters))

	for i, ch := range left.Chapters {
		leftMap[chapterSig(ch)] = entry{ch.Title, i + 1}
	}
	for i, ch := range right.Chapters {
		rightMap[chapterSig(ch)] = entry{ch.Title, i + 1}
	}

	// Chapters only in left.
	var onlyLeft []string
	for _, ch := range left.Chapters {
		sig := chapterSig(ch)
		if _, ok := rightMap[sig]; !ok {
			onlyLeft = append(onlyLeft, fmt.Sprintf("  #%-3d %s (%s)", leftMap[sig].Pos, ch.Title, sig))
		}
	}

	// Chapters only in right.
	var onlyRight []string
	for _, ch := range right.Chapters {
		sig := chapterSig(ch)
		if _, ok := leftMap[sig]; !ok {
			onlyRight = append(onlyRight, fmt.Sprintf("  #%-3d %s (%s)", rightMap[sig].Pos, ch.Title, sig))
		}
	}

	// Chapters in both but at different positions.
	var moved []string
	for _, ch := range left.Chapters {
		sig := chapterSig(ch)
		re, ok := rightMap[sig]
		if !ok {
			continue
		}
		le := leftMap[sig]
		if le.Pos != re.Pos {
			moved = append(moved, fmt.Sprintf("  %s: #%d -> #%d", ch.Title, le.Pos, re.Pos))
		}
	}

	if len(onlyLeft) == 0 && len(onlyRight) == 0 && len(moved) == 0 {
		fmt.Println("The two configs contain the same chapters in the same order.")
		return
	}

	if len(onlyLeft) > 0 {
		fmt.Printf("Only in %s (%d):\n", leftName, len(onlyLeft))
		for _, s := range onlyLeft {
			fmt.Println(s)
		}
		fmt.Println()
	}

	if len(onlyRight) > 0 {
		fmt.Printf("Only in %s (%d):\n", rightName, len(onlyRight))
		for _, s := range onlyRight {
			fmt.Println(s)
		}
		fmt.Println()
	}

	if len(moved) > 0 {
		fmt.Printf("Reordered (%d):\n", len(moved))
		for _, s := range moved {
			fmt.Println(s)
		}
		fmt.Println()
	}

	fmt.Printf("Summary: %d only-left, %d only-right, %d reordered\n",
		len(onlyLeft), len(onlyRight), len(moved))
}
