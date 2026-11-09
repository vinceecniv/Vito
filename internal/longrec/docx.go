package longrec

import (
	"archive/zip"
	"bytes"
	"strings"
	"time"
)

// DOCX writes the document as a Word file: the title and when, headers as
// headings, each paragraph with its time in grey before it, marked sentences
// highlighted, and the marks with their notes at the end. The bytes depend on
// the text only (fixed zip times), so an unchanged document hashes the same.
func (d Doc) DOCX(title, when string) ([]byte, error) {
	var body strings.Builder
	para := func(style string, runs ...string) {
		body.WriteString("<w:p>")
		if style != "" {
			body.WriteString(`<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`)
		}
		for _, r := range runs {
			body.WriteString(r)
		}
		body.WriteString("</w:p>")
	}
	para("Title", run(title, ""))
	para("Subtitle", run(when+" · "+Clock(d.Meta.Seconds), ""))
	for _, it := range d.Items {
		if it.Kind == "header" {
			para("Heading1", run(headerTitle(it), ""))
			continue
		}
		runs := []string{run(Clock(it.At)+" ", `<w:color w:val="8A8F98"/><w:sz w:val="18"/>`)}
		for i, s := range it.Sentences {
			text := s.Text
			if i < len(it.Sentences)-1 {
				text += " "
			}
			if s.Marked {
				runs = append(runs, run(s.Text, `<w:highlight w:val="yellow"/>`))
				if text != s.Text {
					runs = append(runs, run(" ", ""))
				}
				continue
			}
			runs = append(runs, run(text, ""))
		}
		para("", runs...)
	}
	if len(d.Marks) > 0 {
		para("Rule")
		for _, m := range d.Marks {
			runs := []string{run(Clock(m.At), `<w:b/>`), run(" — "+m.Text, "")}
			if m.Note != "" {
				runs = append(runs, run(" ("+m.Note+")", `<w:i/><w:color w:val="5A5F68"/>`))
			}
			para("Mark", runs...)
		}
	}

	files := []struct{ name, data string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`},
		{"word/_rels/document.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{"docProps/core.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + xmlText(title) + `</dc:title><dc:creator>Vito</dc:creator></cp:coreProperties>`},
		{"word/styles.xml", docxStyles},
		{"word/document.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() +
			`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr></w:body></w:document>`},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: stamp})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.data)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// run is one stretch of text with run properties (rPr content).
func run(text, props string) string {
	r := "<w:r>"
	if props != "" {
		r += "<w:rPr>" + props + "</w:rPr>"
	}
	return r + `<w:t xml:space="preserve">` + xmlText(text) + "</w:t></w:r>"
}

// xmlText escapes text for XML and drops the control characters it forbids.
func xmlText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r', r == 0xFFFE, r == 0xFFFF:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

const docxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="Calibri" w:cs="Calibri"/><w:sz w:val="22"/><w:szCs w:val="22"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="160" w:line="288" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>
<w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="60"/></w:pPr><w:rPr><w:b/><w:color w:val="1F2430"/><w:sz w:val="48"/><w:szCs w:val="48"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Subtitle"><w:name w:val="Subtitle"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:after="360"/></w:pPr><w:rPr><w:color w:val="6B7280"/><w:sz w:val="20"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="320" w:after="120"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:color w:val="6D28D9"/><w:sz w:val="30"/><w:szCs w:val="30"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Rule"><w:name w:val="Vito rule"/><w:basedOn w:val="Normal"/><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="1" w:color="D1D5DB"/></w:pBdr><w:spacing w:before="240" w:after="200"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="Mark"><w:name w:val="Vito mark"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:after="80"/></w:pPr></w:style>
</w:styles>`
