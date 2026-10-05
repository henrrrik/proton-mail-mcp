package render

import (
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTMLToText converts an HTML body to readable text. Scripts, styles, images
// and other embedded content are dropped; links become "text (url)" when the
// url adds information and uses a safe scheme.
func HTMLToText(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return ""
	}
	w := &htmlWriter{}
	w.walk(doc, 0)
	return Clean(tidy(w.b.String()))
}

type htmlWriter struct {
	b    strings.Builder
	pre  int
	list []int // per open list: 0 for <ul>, next number for <ol>
}

var skip = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Head: true, atom.Title: true,
	atom.Noscript: true, atom.Template: true, atom.Svg: true, atom.Math: true,
	atom.Img: true, atom.Picture: true, atom.Video: true, atom.Audio: true,
	atom.Iframe: true, atom.Object: true, atom.Embed: true, atom.Canvas: true,
	atom.Form: true, atom.Button: true, atom.Select: true, atom.Input: true,
}

var block = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true,
	atom.Header: true, atom.Footer: true, atom.Table: true, atom.Tr: true,
	atom.Blockquote: true, atom.H1: true, atom.H2: true, atom.H3: true,
	atom.H4: true, atom.H5: true, atom.H6: true, atom.Ul: true, atom.Ol: true,
	atom.Pre: true, atom.Hr: true, atom.Center: true, atom.Address: true,
}

// maxDepth stops hostile, deeply nested HTML from exhausting the stack.
const maxDepth = 256

func (w *htmlWriter) walk(n *html.Node, depth int) {
	if depth > maxDepth {
		return
	}
	switch n.Type {
	case html.TextNode:
		w.text(n.Data)
		return
	case html.CommentNode:
		return
	case html.ElementNode:
		if skip[n.DataAtom] {
			return
		}
		if hidden(n) {
			return
		}
	}

	switch n.DataAtom {
	case atom.Br:
		w.b.WriteByte('\n')
		return
	case atom.A:
		w.link(n, depth)
		return
	case atom.Li:
		w.newline()
		w.b.WriteString(strings.Repeat("  ", max(len(w.list)-1, 0)))
		if k := len(w.list); k > 0 && w.list[k-1] > 0 {
			w.b.WriteString(strconv.Itoa(w.list[k-1]) + ". ")
			w.list[k-1]++
		} else {
			w.b.WriteString("- ")
		}
	case atom.Ul:
		w.list = append(w.list, 0)
		defer func() { w.list = w.list[:len(w.list)-1] }()
	case atom.Ol:
		w.list = append(w.list, 1)
		defer func() { w.list = w.list[:len(w.list)-1] }()
	case atom.Td, atom.Th:
		w.b.WriteByte(' ')
	case atom.Pre:
		w.pre++
		defer func() { w.pre-- }()
	}

	if block[n.DataAtom] {
		w.paragraph()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, depth+1)
	}
	if block[n.DataAtom] {
		w.paragraph()
	}
}

func (w *htmlWriter) text(s string) {
	if w.pre > 0 {
		w.b.WriteString(s)
		return
	}
	w.b.WriteString(spaces.ReplaceAllString(s, " "))
}

func (w *htmlWriter) link(n *html.Node, depth int) {
	inner := &htmlWriter{pre: w.pre}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		inner.walk(c, depth+1)
	}
	text := strings.TrimSpace(inner.b.String())
	href := strings.TrimSpace(attr(n, "href"))
	lower := strings.ToLower(href)
	safe := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "mailto:")
	switch {
	case !safe || href == text || strings.TrimPrefix(lower, "mailto:") == strings.ToLower(text):
		w.text(" " + text + " ")
	case text == "":
		w.text(" " + href + " ")
	default:
		w.text(" " + text + " (" + href + ") ")
	}
}

func (w *htmlWriter) newline() {
	if s := w.b.String(); len(s) > 0 && !strings.HasSuffix(s, "\n") {
		w.b.WriteByte('\n')
	}
}

func (w *htmlWriter) paragraph() {
	s := w.b.String()
	switch {
	case len(s) == 0, strings.HasSuffix(s, "\n\n"):
	case strings.HasSuffix(s, "\n"):
		w.b.WriteByte('\n')
	default:
		w.b.WriteString("\n\n")
	}
}

// hidden catches the common ways newsletters hide preheader text and
// tracking elements.
func hidden(n *html.Node) bool {
	if _, ok := attrOK(n, "hidden"); ok {
		return true
	}
	style := strings.ToLower(strings.ReplaceAll(attr(n, "style"), " ", ""))
	return strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden")
}

func attr(n *html.Node, key string) string {
	v, _ := attrOK(n, key)
	return v
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

var (
	spaces     = regexp.MustCompile(`[ \t\r\n\f\x{a0}]+`)
	listMarker = regexp.MustCompile(`^(- |\d+\. )`)
)

// tidy collapses the spaces left by inline elements. List indentation is
// kept; other leading space is dropped.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		indent := ""
		if listMarker.MatchString(trimmed) {
			indent = line[:len(line)-len(trimmed)]
		}
		lines[i] = indent + strings.Join(strings.Fields(trimmed), " ")
	}
	return strings.Join(lines, "\n")
}
