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
