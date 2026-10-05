// Package render turns raw email into text that is safe to hand to a model:
// MIME parsing, HTML to text, stripping inline data, and wrapping everything
// email-derived in an untrusted block.
package render

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const tag = "untrusted_email"

// brackets maps every angle bracket, including fullwidth and small forms,
// to a look-alike that cannot form a tag. Escaped text can then never close
// the untrusted block or open a fake one, however the tag is spelled.
var brackets = strings.NewReplacer("<", "‹", ">", "›", "＜", "‹", "＞", "›", "﹤", "‹", "﹥", "›")

// Escape neutralises tag-forming characters in email-derived text.
func Escape(s string) string {
	return brackets.Replace(s)
}

// Wrap places email-derived text in a delimited untrusted block. id must be
// server-generated (a message id), never email content.
func Wrap(id, s string) string {
	return "<" + tag + ` id="` + Escape(strings.ReplaceAll(id, `"`, "'")) + `">` + "\n" + Escape(s) + "\n</" + tag + ">"
}

// OneLine flattens a header value so it cannot fake extra header lines.
func OneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
}

var dataURI = regexp.MustCompile(`(?i)\bdata:[a-z0-9.+-]*/?[a-z0-9.+-]*[;,][^\s"'<>)\]]*`)

// Clean removes inline data URIs, normalises line endings and trims runs of
// blank lines.
func Clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = dataURI.ReplaceAllString(s, "[inline data removed]")
	var b strings.Builder
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t ")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// Truncate cuts s to at most max runes, reporting whether it cut anything.
func Truncate(s string, max int) (string, bool) {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s, false
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i], true
		}
		n++
	}
	return s, false
}
