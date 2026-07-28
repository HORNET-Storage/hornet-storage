package search

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/nbd-wtf/go-nostr"
	"golang.org/x/text/unicode/norm"
)

// SearchQuery represents a parsed search query with text and extensions.
type SearchQuery struct {
	Text       string            // The main search text (without extensions)
	Extensions map[string]string // Key-value pairs for extensions like include:spam
}

var (
	extensionRegex = regexp.MustCompile(`\b([\pL\pN_]+):(\S+)\b`)
	phraseRegex    = regexp.MustCompile(`"([^"]+)"`)
)

// ParseSearchQuery parses a NIP-50 search string. Extensions are deliberately
// removed from the searchable text even when HORNETS does not implement them; this
// prevents an unsupported extension from accidentally becoming a required term.
func ParseSearchQuery(value string) SearchQuery {
	query := SearchQuery{Extensions: make(map[string]string)}
	remaining := value

	for _, match := range extensionRegex.FindAllStringSubmatch(value, -1) {
		if len(match) < 3 {
			continue
		}
		query.Extensions[strings.ToLower(match[1])] = strings.ToLower(match[2])
		remaining = strings.Replace(remaining, match[0], "", 1)
	}

	query.Text = strings.Join(strings.Fields(remaining), " ")
	return query
}

// NormalizeText applies the same Unicode compatibility and case normalization to
// indexed documents, queries, and live-event checks.
func NormalizeText(value string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(strings.ToLower(value))), " ")
}

// Tokenize returns Unicode word tokens without discarding short terms. Repository
// names and organization names often contain meaningful one- or two-character parts.
func Tokenize(value string) []string {
	normalized := NormalizeText(value)
	var tokens []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}

	for _, r := range normalized {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

// SplitSearchText separates quoted phrases from ordinary prefix terms.
func SplitSearchText(value string) (terms []string, phrases []string) {
	remaining := value
	for _, match := range phraseRegex.FindAllStringSubmatch(value, -1) {
		if len(match) < 2 {
			continue
		}
		phrase := NormalizeText(match[1])
		if phrase != "" {
			phrases = append(phrases, phrase)
		}
		remaining = strings.Replace(remaining, match[0], " ", 1)
	}
	return Tokenize(remaining), phrases
}

// MatchesText mirrors the term-prefix and quoted-phrase semantics used by the Bleve
// query builder. It is used as the trust-minimized canonical check after hydration
// and for live subscriptions, so a stale or malicious index cannot invent matches.
func MatchesText(content, queryText string) bool {
	terms, phrases := SplitSearchText(queryText)
	contentTokens := Tokenize(content)

	for _, term := range terms {
		matched := false
		for _, token := range contentTokens {
			if strings.HasPrefix(token, term) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	for _, phrase := range phrases {
		phraseTokens := Tokenize(phrase)
		if len(phraseTokens) == 0 || !containsTokenSequence(contentTokens, phraseTokens) {
			return false
		}
	}

	return len(terms) > 0 || len(phrases) > 0
}

func containsTokenSequence(content, phrase []string) bool {
	if len(phrase) > len(content) {
		return false
	}
	for start := 0; start <= len(content)-len(phrase); start++ {
		matched := true
		for i := range phrase {
			if content[start+i] != phrase[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// EventMatchesFilter applies canonical Nostr filter semantics, including prefix
// matching for ids/authors and the normalized NIP-50 content predicate.
func EventMatchesFilter(event *nostr.Event, filter nostr.Filter) bool {
	if len(filter.IDs) > 0 && !matchesAnyPrefix(filter.IDs, event.ID) {
		return false
	}
	if len(filter.Authors) > 0 && !matchesAnyPrefix(filter.Authors, event.PubKey) {
		return false
	}
	if len(filter.Kinds) > 0 && !containsKind(filter.Kinds, event.Kind) {
		return false
	}
	if filter.Since != nil && event.CreatedAt < *filter.Since {
		return false
	}
	if filter.Until != nil && event.CreatedAt > *filter.Until {
		return false
	}

	for rawName, values := range filter.Tags {
		name := strings.TrimPrefix(rawName, "#")
		matched := false
		for _, tag := range event.Tags {
			if len(tag) < 2 || tag[0] != name {
				continue
			}
			for _, value := range values {
				if tag[1] == value {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}

	if filter.Search != "" {
		parsed := ParseSearchQuery(filter.Search)
		if parsed.Text == "" || !MatchesText(event.Content, parsed.Text) {
			return false
		}
	}
	return true
}

func matchesAnyPrefix(prefixes []string, value string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func containsKind(kinds []int, kind int) bool {
	for _, candidate := range kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

// HasExtension checks if a specific extension exists in the query.
func (q *SearchQuery) HasExtension(key string) bool {
	_, exists := q.Extensions[strings.ToLower(key)]
	return exists
}

// GetExtension returns the value of a specific extension.
func (q *SearchQuery) GetExtension(key string) (string, bool) {
	value, exists := q.Extensions[strings.ToLower(key)]
	return value, exists
}

// IsSpamIncluded returns true if the search should include spam results.
func (q *SearchQuery) IsSpamIncluded() bool {
	value, exists := q.GetExtension("include")
	return exists && value == "spam"
}
