package main

import (
	"fmt"
	"strings"
)

const containerXML = `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

func buildOPF(bookID, title, author string, manifestItems, spineItems []string, hasChapters bool) string {
	if author == "" {
		author = "Unknown"
	}
	guideBegin := ""
	if hasChapters {
		guideBegin = "\n    <reference type=\"text\" title=\"Begin Reading\" href=\"Text/chapter_001.html\"/>"
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="BookId">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>%s</dc:title>
    <dc:creator opf:role="aut">%s</dc:creator>
    <dc:language>en</dc:language>
    <dc:identifier id="BookId">%s</dc:identifier>
    <meta name="cover" content="cover-image"/>
  </metadata>
  <manifest>
    <item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
    <item id="cover-image" href="Images/cover.jpg" media-type="image/jpeg"/>
%s
  </manifest>
  <spine toc="ncx">
%s
  </spine>
  <guide>
    <reference type="cover" title="Cover" href="Text/cover.html"/>
    <reference type="toc" title="Table of Contents" href="Text/toc.html"/>%s
  </guide>
</package>`,
		xmlEscape(title),
		xmlEscape(author),
		bookID,
		strings.Join(manifestItems, "\n"),
		strings.Join(spineItems, "\n"),
		guideBegin,
	)
}

func buildNCX(bookID, title string, navPoints, pageTargets []string) string {
	totalPages := len(pageTargets)
	pageList := ""
	if totalPages > 0 {
		pageList = fmt.Sprintf("\n  <pageList>\n%s\n  </pageList>",
			strings.Join(pageTargets, "\n"))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE ncx PUBLIC "-//NISO//DTD ncx 2005-1//EN" "http://www.daisy.org/z3986/2005/ncx-2005-1.dtd">
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1">
  <head>
    <meta name="dtb:uid" content="%s"/>
    <meta name="dtb:depth" content="1"/>
    <meta name="dtb:totalPageCount" content="%d"/>
    <meta name="dtb:maxPageNumber" content="%d"/>
  </head>
  <docTitle><text>%s</text></docTitle>
  <navMap>
%s
  </navMap>%s
</ncx>`,
		bookID,
		totalPages,
		totalPages,
		xmlEscape(title),
		strings.Join(navPoints, "\n"),
		pageList,
	)
}
