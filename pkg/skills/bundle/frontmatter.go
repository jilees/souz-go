package bundle

import (
	"fmt"
	"strings"
)

// Manifest is the parsed SKILL.md YAML frontmatter. Only a flat subset of
// YAML is supported (top-level scalars plus one nested "metadata" map),
// matching the original Kotlin implementation's bespoke parser — a real
// YAML parser would accept this same subset, so bundles remain portable if
// one is swapped in later.
type Manifest struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Author      string            `json:"author,omitempty"`
	Version     string            `json:"version,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// parseFrontmatter splits SKILL.md into its YAML-ish frontmatter and
// markdown body. Frontmatter is delimited by a line containing exactly
// "---" at the very start of the file and the next such line.
func parseFrontmatter(content string) (manifest Manifest, raw string, body string, err error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Manifest{}, "", "", fmt.Errorf("SKILL.md must start with a \"---\" frontmatter delimiter")
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return Manifest{}, "", "", fmt.Errorf("SKILL.md frontmatter has no closing \"---\"")
	}

	frontmatterLines := lines[1:end]
	raw = strings.Join(frontmatterLines, "\n")
	body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))

	manifest, err = parseManifestLines(frontmatterLines)
	if err != nil {
		return Manifest{}, "", "", err
	}
	return manifest, raw, body, nil
}

func parseManifestLines(lines []string) (Manifest, error) {
	m := Manifest{}
	i := 0
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			i++
			continue
		}
		if line != strings.TrimLeft(line, " \t") {
			// An indented line outside of a "metadata:" block; skip it
			// rather than fail — unknown/malformed lines are ignored,
			// matching the original's lenient parsing.
			i++
			continue
		}

		key, value, ok := splitKeyValue(line)
		if !ok {
			i++
			continue
		}

		if key == "metadata" {
			meta, consumed := parseMetadataBlock(lines[i+1:])
			m.Metadata = meta
			i += 1 + consumed
			continue
		}

		switch key {
		case "name":
			m.Name = value
		case "description":
			m.Description = value
		case "author":
			m.Author = value
		case "version":
			m.Version = value
		}
		i++
	}

	if strings.TrimSpace(m.Name) == "" {
		return Manifest{}, fmt.Errorf("SKILL.md frontmatter: \"name\" is required")
	}
	if strings.TrimSpace(m.Description) == "" {
		return Manifest{}, fmt.Errorf("SKILL.md frontmatter: \"description\" is required")
	}
	return m, nil
}

// parseMetadataBlock reads indented "key: value" lines immediately
// following a "metadata:" line, stopping at the first non-indented or blank
// line. Returns the parsed map and how many lines were consumed.
func parseMetadataBlock(lines []string) (map[string]string, int) {
	meta := map[string]string{}
	consumed := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			break
		}
		if line == strings.TrimLeft(line, " \t") {
			break // dedented: end of the metadata block
		}
		key, value, ok := splitKeyValue(strings.TrimLeft(line, " \t"))
		if ok {
			meta[key] = value
		}
		consumed++
	}
	if len(meta) == 0 {
		return nil, consumed
	}
	return meta, consumed
}

func splitKeyValue(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	value = unquote(value)
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
