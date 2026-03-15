package main

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
)

var (
	reAnyCSS       = regexp.MustCompile(`<link[^>]+rel="stylesheet"[^>]*/?>`)
	rePageTemplate = regexp.MustCompile(`\s*<link[^>]*page-template\.xpgt[^>]*/?>`)
	reImgSrc       = regexp.MustCompile(`src="[^"]+\.(jpg|jpeg|png|gif|svg)"`)
	reTOCLink      = regexp.MustCompile(`<a[^>]+href="[^"]*"[^>]*>(<span[^>]*>[^<]*</span>)</a>`)
	isImageFile    = regexp.MustCompile(`(?i)\.(jpg|jpeg|png|gif|svg)$`)
)

// rewriteHTML updates asset paths in a source chapter to match the output
// epub structure. CSS links are replaced with the book's output stylesheet,
// image src paths are rewritten to the book's image subdirectory, and any
// unsupported link types (e.g. Adobe page templates) are stripped.
func rewriteHTML(src []byte, bk BookConfig) []byte {
	src = rePageTemplate.ReplaceAll(src, nil)

	if bk.CSSDest != "" {
		newLink := fmt.Sprintf(`<link href="../Styles/%s" rel="stylesheet" type="text/css"/>`, bk.CSSDest)
		src = reAnyCSS.ReplaceAll(src, []byte(newLink))
	}

	if bk.ImageDest != "" {
		src = reImgSrc.ReplaceAllFunc(src, func(match []byte) []byte {
			s := string(match)
			fname := path.Base(s[5 : len(s)-1]) // strip src=" and trailing "
			return []byte(fmt.Sprintf(`src="../Images/%s/%s"`, bk.ImageDest, fname))
		})
	}

	if bk.StripTOCLinks {
		src = reTOCLink.ReplaceAll(src, []byte(`$1`))
	}

	return src
}

// extractBodyContent returns the content between <body...> and </body> tags.
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

// appendAfterBodyTag inserts content immediately after the opening <body> tag.
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

// insertBeforeBodyClose inserts content immediately before the closing
// </body> tag. Returns the original content unchanged if no </body> is found.
func insertBeforeBodyClose(src []byte, insert []byte) []byte {
	idx := bytes.LastIndex(src, []byte("</body>"))
	if idx < 0 {
		return src
	}
	result := make([]byte, 0, len(src)+len(insert)+1)
	result = append(result, src[:idx]...)
	result = append(result, insert...)
	result = append(result, '\n')
	result = append(result, src[idx:]...)
	return result
}

// wordsPerPage controls the approximate word count between page break markers.
// The default of 500 produces page numbers that advance roughly one per
// screen tap on Kindle at typical font sizes. Lower values (e.g. 250) give
// finer-grained pages matching printed paperback density; higher values give
// coarser pages. Can be overridden via the -words-per-page flag.
var wordsPerPage = 500

// PageRef describes a single page marker's location within a chapter file.
type PageRef struct {
	Num      int    // Page number (1-indexed, globally unique across the book).
	DestFile string // Chapter filename, e.g. "chapter_001.html".
	Fragment string // Anchor ID, e.g. "page_42". Empty for the chapter start.
}

// insertPageMarkers walks the HTML body content, counting words in text runs
// (outside tags), and inserts an invisible anchor every wordsPerPage words.
// startPage is the first page number to assign; destFile is the chapter's
// output filename used in PageRef. Returns the modified HTML and a slice of
// PageRefs for all pages in this chapter (including one at the chapter start).
func insertPageMarkers(src []byte, startPage int, destFile string) ([]byte, []PageRef) {
	// Always emit a page ref at the chapter start (no fragment needed).
	refs := []PageRef{{Num: startPage, DestFile: destFile}}
	page := startPage + 1

	// Find the body content boundaries so we only count words inside <body>.
	bodyStart := bytes.Index(src, []byte("<body"))
	if bodyStart < 0 {
		return src, refs
	}
	bodyTagEnd := bytes.IndexByte(src[bodyStart:], '>')
	if bodyTagEnd < 0 {
		return src, refs
	}
	contentStart := bodyStart + bodyTagEnd + 1
	bodyEnd := bytes.LastIndex(src, []byte("</body>"))
	if bodyEnd < 0 || bodyEnd <= contentStart {
		return src, refs
	}

	body := src[contentStart:bodyEnd]

	var out bytes.Buffer
	out.Grow(len(body) + 1024) // Preallocate with room for markers.

	wordCount := 0
	inTag := false
	inWord := false

	for i := 0; i < len(body); i++ {
		c := body[i]

		if c == '<' {
			inTag = true
			inWord = false
			out.WriteByte(c)
			continue
		}
		if c == '>' {
			inTag = false
			out.WriteByte(c)
			continue
		}

		if inTag {
			out.WriteByte(c)
			continue
		}

		// Text content: count word boundaries.
		isSpace := c == ' ' || c == '\t' || c == '\n' || c == '\r'

		if !isSpace && !inWord {
			// Starting a new word.
			wordCount++
			inWord = true

			if wordCount >= wordsPerPage {
				// Insert a page marker before this word.
				fragment := fmt.Sprintf("page_%d", page)
				fmt.Fprintf(&out, `<span id="%s"/>`, fragment)
				refs = append(refs, PageRef{Num: page, DestFile: destFile, Fragment: fragment})
				page++
				wordCount = 0
			}
		} else if isSpace {
			inWord = false
		}

		out.WriteByte(c)
	}

	// Reassemble: prefix + modified body + suffix.
	var result bytes.Buffer
	result.Grow(len(src) + out.Len() - len(body))
	result.Write(src[:contentStart])
	result.Write(out.Bytes())
	result.Write(src[bodyEnd:])

	return result.Bytes(), refs
}
