package notify

import "strings"

// aiBlocks holds the four sections Gemini emits per the prompt in
// internal/ai/prompt.txt. Each value is the body text without the header line.
// Empty fields mean the parser couldn't find that block.
type aiBlocks struct {
	Sleep          string
	Yesterday      string
	Recovery       string
	Recommendation string
	// Raw is the full text — used as a fallback when no headers parse out.
	Raw string
}

// canonical block key → header tokens we recognise across en/ru/sr.
// Match is case-insensitive on a trimmed, punctuation-stripped line.
var aiHeaderTokens = map[string][]string{
	"sleep":          {"sleep", "сон", "san"},
	"yesterday":      {"yesterday", "вчера", "juče", "juce"},
	"recovery":       {"recovery", "восстановление", "oporavak"},
	"recommendation": {"recommendation", "рекомендация", "preporuka"},
}

func normaliseHeader(line string) string {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, ":：—-•")
	line = strings.TrimSpace(line)
	return strings.ToLower(line)
}

func headerKey(line string) string {
	norm := normaliseHeader(line)
	if norm == "" || len(norm) > 30 {
		return ""
	}
	for key, toks := range aiHeaderTokens {
		for _, t := range toks {
			if norm == t {
				return key
			}
		}
	}
	return ""
}

// parseAIInsight splits Gemini's output into per-section blocks. If no headers
// match, the entire text is returned in Raw and all section fields stay empty —
// callers should fall back to printing Raw as a single italic blob.
func parseAIInsight(text string) aiBlocks {
	out := aiBlocks{Raw: strings.TrimSpace(text)}
	if out.Raw == "" {
		return out
	}

	lines := strings.Split(out.Raw, "\n")
	current := ""
	var buf strings.Builder

	flush := func() {
		body := strings.TrimSpace(buf.String())
		buf.Reset()
		if current == "" || body == "" {
			return
		}
		switch current {
		case "sleep":
			out.Sleep = body
		case "yesterday":
			out.Yesterday = body
		case "recovery":
			out.Recovery = body
		case "recommendation":
			out.Recommendation = body
		}
	}

	for _, line := range lines {
		if key := headerKey(line); key != "" {
			flush()
			current = key
			continue
		}
		if current == "" {
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	flush()
	return out
}

// hasAnyBlock reports whether the parser found at least one labelled block.
func (a aiBlocks) hasAnyBlock() bool {
	return a.Sleep != "" || a.Yesterday != "" || a.Recovery != "" || a.Recommendation != ""
}
