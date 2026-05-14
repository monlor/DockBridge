package diagnostics

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Redactor struct {
	patterns []*regexp.Regexp
}

func NewRedactor() Redactor {
	return Redactor{patterns: []*regexp.Regexp{
		regexp.MustCompile(`(?i)(token|password|secret|key)=([^,\s]+)`),
		regexp.MustCompile(`/[^\s]*\.ssh/[^\s]+`),
	}}
}

func (r Redactor) String(value string) string {
	out := value
	for _, pattern := range r.patterns {
		out = pattern.ReplaceAllStringFunc(out, func(match string) string {
			if strings.Contains(match, "=") {
				key, _, _ := strings.Cut(match, "=")
				return key + "=<redacted>"
			}
			return "<redacted-path>"
		})
	}
	return out
}

type Reporter struct {
	redactor Redactor
	entries  []entry
}

type entry struct {
	kind   string
	fields map[string]string
}

func NewReporter(redactor Redactor) *Reporter {
	return &Reporter{redactor: redactor}
}

func (r *Reporter) Add(kind string, fields map[string]string) {
	copied := make(map[string]string, len(fields))
	for key, value := range fields {
		copied[key] = r.redactor.String(value)
	}
	r.entries = append(r.entries, entry{kind: kind, fields: copied})
}

func (r *Reporter) String() string {
	var lines []string
	for _, entry := range r.entries {
		keys := make([]string, 0, len(entry.fields))
		for key := range entry.fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", key, entry.fields[key]))
		}
		lines = append(lines, entry.kind+": "+strings.Join(parts, " "))
	}
	return strings.Join(lines, "\n")
}
