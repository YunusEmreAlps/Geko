package web

import (
	"html/template"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type sourceLink struct {
	Location string
	URL      template.URL
}

var (
	pythonFrame   = regexp.MustCompile("^File \"([^\"]+)\", line ([0-9]+)(?:,.*)?$")
	dotnetFrame   = regexp.MustCompile(" in (.+):line ([0-9]+)$")
	positionFrame = regexp.MustCompile("^(.*?):([0-9]+)(?::([0-9]+))?$")
	windowsSource = regexp.MustCompile("^[A-Za-z]:/")
)

func sourceLinks(value any) []sourceLink {
	stack, ok := value.(string)
	if !ok {
		return nil
	}
	var links []sourceLink
	seen := make(map[string]bool)
	for raw := range strings.SplitSeq(stack, "\n") {
		file, line, column := sourcePosition(strings.TrimSpace(raw))
		file = strings.ReplaceAll(file, "\\", "/")
		if !absoluteSourcePath(file) || !validPosition(line) || column != "" && !validPosition(column) {
			continue
		}
		location := file + ":" + line
		if column != "" {
			location += ":" + column
		}
		if seen[location] {
			continue
		}
		seen[location] = true
		// The scheme and authority are fixed. Telemetry supplies only an escaped path.
		target := url.URL{Scheme: "vscode", Host: "file", Path: "/" + strings.TrimPrefix(location, "/")}
		links = append(links, sourceLink{location, template.URL(target.String())})
		if len(links) == 20 {
			break
		}
	}
	return links
}

func sourcePosition(frame string) (file, line, column string) {
	for _, pattern := range []*regexp.Regexp{pythonFrame, dotnetFrame} {
		if match := pattern.FindStringSubmatch(frame); match != nil {
			return match[1], match[2], ""
		}
	}
	frame = strings.TrimPrefix(frame, "at ")
	if strings.HasSuffix(frame, ")") {
		if start := strings.Index(frame, " ("); start >= 0 {
			frame = frame[start+2 : len(frame)-1]
		}
	}
	if offset := strings.LastIndex(frame, " +0x"); offset >= 0 {
		frame = frame[:offset]
	}
	if match := positionFrame.FindStringSubmatch(frame); match != nil {
		return match[1], match[2], match[3]
	}
	return "", "", ""
}

func absoluteSourcePath(file string) bool {
	windows := windowsSource.MatchString(file)
	if !windows && (!strings.HasPrefix(file, "/") || strings.HasPrefix(file, "//")) {
		return false
	}
	if windows {
		file = file[2:]
	}
	if len(file) > 1024 || path.Clean(file) != file || strings.ContainsAny(file, ":?#%<>|*\"") ||
		strings.ContainsFunc(file, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return false
	}
	if windows {
		for segment := range strings.SplitSeq(file, "/") {
			if strings.HasSuffix(segment, " ") || strings.HasSuffix(segment, ".") {
				return false
			}
		}
	}
	switch strings.ToLower(path.Ext(file)) {
	case ".go", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".py", ".java", ".kt", ".kts",
		".cs", ".fs", ".vb", ".rb", ".php", ".rs", ".c", ".h", ".cc", ".cpp", ".hpp", ".swift":
		return true
	}
	return false
}

func validPosition(value string) bool {
	number, err := strconv.ParseUint(value, 10, 31)
	return err == nil && number > 0
}
