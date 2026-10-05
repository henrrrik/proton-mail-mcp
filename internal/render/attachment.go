package render

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// textTypes are non-text/* content types whose bodies are readable text.
var textTypes = map[string]bool{
	"application/json": true, "application/xml": true, "application/csv": true,
	"application/x-yaml": true, "application/yaml": true, "application/ics": true,
	"application/x-sh": true, "application/javascript": true, "application/toml": true,
}

var textExts = map[string]bool{
	".txt": true, ".md": true, ".csv": true, ".tsv": true, ".json": true, ".xml": true,
	".yaml": true, ".yml": true, ".ics": true, ".log": true, ".toml": true, ".eml": true,
}

// AttachmentText extracts readable text from an attachment. ok is false for
// binary formats, for which callers should return metadata only.
func AttachmentText(body []byte, contentType, filename string) (text string, ok bool, err error) {
	ct := strings.ToLower(contentType)
	ext := strings.ToLower(filepath.Ext(filename))
	switch {
	case ct == "text/html" || ext == ".html" || ext == ".htm":
		return HTMLToText(string(body)), true, nil
	case ct == "application/pdf" || ext == ".pdf":
		t, err := pdfText(body)
		if err != nil {
			return "", false, err
		}
		return Clean(t), true, nil
	case strings.HasPrefix(ct, "text/") || ct == "message/rfc822" || textTypes[ct] || textExts[ext]:
		if !utf8.Valid(body) {
			body = bytes.ToValidUTF8(body, []byte("�"))
		}
		return Clean(string(body)), true, nil
	}
	return "", false, nil
}

// PDF limits: the parser has no budget of its own.
const (
	maxPDFBytes = 10 << 20
	pdfTimeout  = 10 * time.Second
)

func pdfText(body []byte) (string, error) {
	if len(body) > maxPDFBytes {
		return "", fmt.Errorf("PDF is larger than %d MB", maxPDFBytes>>20)
	}
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		// The parser panics on some malformed files.
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: errors.New("unreadable PDF")}
			}
		}()
		r, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			done <- result{err: errors.New("unreadable PDF")}
			return
		}
		tr, err := r.GetPlainText()
		if err != nil {
			done <- result{err: errors.New("unreadable PDF")}
			return
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxPartBytes))
		done <- result{string(b), err}
	}()
	select {
	case res := <-done:
		return res.text, res.err
	case <-time.After(pdfTimeout):
		// The goroutine cannot be stopped; it is abandoned and its result dropped.
		return "", errors.New("PDF text extraction timed out")
	}
}
