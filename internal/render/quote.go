package render

import (
	"regexp"
	"strings"
)

// Lines that introduce quoted history in common clients, including Proton's
// "------- Original Message -------".
var quoteHeader = regexp.MustCompile(`(?i)^(-{2,}\s*(original|forwarded) message\s*-{2,}|(on|den|op|am|le) .{4,300} (wrote|skrev|schreef|schrieb|a écrit)( .{0,200})?:|from: .+)$`)

// TrimQuoted drops quoted reply history from a plain-text body: everything
// from the first quote header on, and any ">"-prefixed lines before it.
func TrimQuoted(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i, line := range lines {
		t := strings.TrimSpace(line)
		isFrom := strings.HasPrefix(strings.ToLower(t), "from:")
		if quoteHeader.MatchString(t) && (!isFrom || hasQuoteAfter(lines[i+1:])) {
			break
		}
		if strings.HasPrefix(t, ">") {
			continue
		}
		out = append(out, line)
	}
	trimmed := strings.TrimSpace(strings.Join(out, "\n"))
	if trimmed == "" {
		return strings.TrimSpace(s)
	}
	return trimmed
}

// A bare "From:" line only counts as a quote header when it is followed by the
// usual Sent/To/Subject block.
func hasQuoteAfter(rest []string) bool {
	for _, l := range rest[:min(len(rest), 4)] {
		l = strings.ToLower(strings.TrimSpace(l))
		if strings.HasPrefix(l, "sent:") || strings.HasPrefix(l, "date:") || strings.HasPrefix(l, "subject:") || strings.HasPrefix(l, "to:") {
			return true
		}
	}
	return false
}

// Quote prefixes every line with "> " for a reply.
func Quote(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, ">") {
			lines[i] = ">" + l
		} else if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}
