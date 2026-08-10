package service

import (
	"archive/zip"
	"bytes"
	"testing"
)

func TestValidateAttachmentRejectsExtensionMIMEMismatch(t *testing.T) {
	_, _, err := validateAttachment(".png", []byte("plain text"))
	if err != ErrAttachmentMIMEMismatch {
		t.Fatalf("expected MIME mismatch, got %v", err)
	}
}

func TestValidateAndExtractTXT(t *testing.T) {
	mimeType, kind, err := validateAttachment(".txt", []byte("你好 Agent"))
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "text/plain" || kind != "document" {
		t.Fatalf("unexpected type: %s %s", mimeType, kind)
	}
	text, _, err := extractDocument(".txt", []byte("你好 Agent"))
	if err != nil || text != "你好 Agent" {
		t.Fatalf("unexpected extraction: %q %v", text, err)
	}
}

func TestValidateAndExtractDOCX(t *testing.T) {
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	file, err := writer.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte(`<w:document xmlns:w="urn:test"><w:body><w:p><w:r><w:t>第一段</w:t></w:r></w:p><w:p><w:r><w:t>第二段</w:t></w:r></w:p></w:body></w:document>`))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	mimeType, kind, err := validateAttachment(".docx", data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if mimeType != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || kind != "document" {
		t.Fatalf("unexpected type: %s %s", mimeType, kind)
	}
	text, _, err := extractDocument(".docx", data.Bytes())
	if err != nil || text != "第一段\n第二段\n" {
		t.Fatalf("unexpected extraction: %q %v", text, err)
	}
}
