package service

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"unicode"

	"github.com/dslipak/pdf"
)

// documentBlock keeps the source structure that retrieval citations will need
// later. Character offsets are rune offsets in extractedDocument.Text.
type documentBlock struct {
	Text         string
	PageNumber   *int
	SectionTitle string
	StartChar    int
	EndChar      int
}

type extractedDocument struct {
	Text   string
	Blocks []documentBlock
}

type rawDocumentBlock struct {
	text         string
	pageNumber   *int
	sectionTitle string
}

func extractStructuredDocument(extension string, data []byte) (extractedDocument, string, error) {
	var (
		blocks []rawDocumentBlock
		err    error
	)
	switch extension {
	case ".txt":
		blocks = textBlocks(string(data), nil)
	case ".docx":
		blocks, err = docxBlocks(data)
	case ".pdf":
		blocks, err = pdfBlocks(data)
	default:
		return extractedDocument{}, "unsupported_type", ErrAttachmentTypeNotAllowed
	}
	if err != nil {
		return extractedDocument{}, "invalid_document", err
	}
	document := assembleDocument(blocks)
	if strings.TrimSpace(document.Text) == "" {
		return extractedDocument{}, "no_extractable_text", errors.New("no extractable text")
	}
	return document, "", nil
}

func assembleDocument(raw []rawDocumentBlock) extractedDocument {
	var output strings.Builder
	blocks := make([]documentBlock, 0, len(raw))
	position := 0
	for _, item := range raw {
		text := strings.TrimSpace(normalizeDocumentText(item.text))
		if text == "" {
			continue
		}
		if output.Len() > 0 {
			output.WriteString("\n\n")
			position += 2
		}
		start := position
		output.WriteString(text)
		position += len([]rune(text))
		blocks = append(blocks, documentBlock{
			Text: text, PageNumber: item.pageNumber, SectionTitle: item.sectionTitle,
			StartChar: start, EndChar: position,
		})
	}
	return extractedDocument{Text: output.String(), Blocks: blocks}
}

func textBlocks(text string, page *int) []rawDocumentBlock {
	paragraphs := strings.FieldsFunc(strings.ReplaceAll(text, "\r\n", "\n"), func(r rune) bool { return r == '\n' })
	blocks := make([]rawDocumentBlock, 0, len(paragraphs))
	section := ""
	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		if heading, ok := inferHeading(paragraph); ok {
			section = heading
		}
		blocks = append(blocks, rawDocumentBlock{text: paragraph, pageNumber: cloneInt(page), sectionTitle: section})
	}
	return blocks
}

func inferHeading(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "#") {
		heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		return heading, heading != ""
	}
	runes := []rune(trimmed)
	if len(runes) > 0 && len(runes) <= 60 && (strings.HasSuffix(trimmed, "：") || strings.HasSuffix(trimmed, ":")) {
		return strings.TrimRight(trimmed, "：:"), true
	}
	return "", false
}

func docxBlocks(data []byte) ([]rawDocumentBlock, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, file := range reader.File {
		if file.Name != "word/document.xml" {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer stream.Close()
		decoder := xml.NewDecoder(stream)
		var blocks []rawDocumentBlock
		var paragraph strings.Builder
		style, section := "", ""
		insideParagraph := false
		for {
			token, tokenErr := decoder.Token()
			if errors.Is(tokenErr, io.EOF) {
				break
			}
			if tokenErr != nil {
				return nil, tokenErr
			}
			switch item := token.(type) {
			case xml.StartElement:
				switch item.Name.Local {
				case "p":
					insideParagraph = true
					paragraph.Reset()
					style = ""
				case "pStyle":
					for _, attribute := range item.Attr {
						if attribute.Name.Local == "val" {
							style = attribute.Value
						}
					}
				case "tab":
					if insideParagraph {
						paragraph.WriteByte('\t')
					}
				}
			case xml.CharData:
				if insideParagraph {
					paragraph.Write([]byte(item))
				}
			case xml.EndElement:
				if item.Name.Local == "p" {
					text := strings.TrimSpace(paragraph.String())
					if text != "" {
						if isHeadingStyle(style) {
							section = text
						} else if heading, ok := inferHeading(text); ok {
							section = heading
						}
						blocks = append(blocks, rawDocumentBlock{text: text, sectionTitle: section})
					}
					insideParagraph = false
				}
			}
		}
		return blocks, nil
	}
	return nil, errors.New("word/document.xml missing")
}

func isHeadingStyle(style string) bool {
	style = strings.ToLower(strings.TrimSpace(style))
	return strings.HasPrefix(style, "heading") || strings.HasPrefix(style, "标题")
}

func pdfBlocks(data []byte) ([]rawDocumentBlock, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	blocks := make([]rawDocumentBlock, 0, reader.NumPage())
	section := ""
	for pageNumber := 1; pageNumber <= reader.NumPage(); pageNumber++ {
		pageText, pageErr := reader.Page(pageNumber).GetPlainText(nil)
		if pageErr != nil {
			return nil, pageErr
		}
		page := pageNumber
		pageBlocks := textBlocks(pageText, &page)
		for index := range pageBlocks {
			if pageBlocks[index].sectionTitle != "" {
				section = pageBlocks[index].sectionTitle
			} else {
				pageBlocks[index].sectionTitle = section
			}
		}
		blocks = append(blocks, pageBlocks...)
	}
	return blocks, nil
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func normalizeDocumentText(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\u0000' || (!unicode.IsPrint(r) && !unicode.IsSpace(r)) {
			return -1
		}
		return r
	}, text)
}
