// Package privacy keeps secrets out of the messages sent to a model without
// making those messages useless. A guard replaces every detected value with a
// category-tagged token (⟦IP_1⟧, ⟦USER_1⟧, ⟦SECRET_1⟧...), the model reasons
// about the tokens, and the same guard restores the original text in the
// streamed answer and in tool arguments before anything is shown or executed.
//
// The mapping lives in memory for the run: the request is tokenized again on
// every turn, so nothing about the secrets has to be persisted, and local
// history stays readable.
package privacy

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Category tags a token and groups the audit rows. The tag itself is visible
// to the model, which is the point: it knows ⟦IP_1⟧ is an address without
// knowing which one.
type Category string

const (
	CategoryIP     Category = "IP"
	CategoryUser   Category = "USER"
	CategoryHost   Category = "HOST"
	CategorySecret Category = "SECRET"
	CategoryKey    Category = "KEY"
	CategoryCert   Category = "CERT"
	CategoryEmail  Category = "EMAIL"
	CategoryCustom Category = "CUSTOM"
)

const (
	TokenOpen  = "⟦"
	TokenClose = "⟧"
)

// Known is one exact value that must never leave the device: a saved password,
// the current host's address or user name, or a value the user listed. A
// boundary keeps short names from matching inside longer words.
type Known struct {
	Value    string
	Category Category
	Boundary bool
}

// Config selects the detectors. Known values are exact matches and need no
// pattern; the toggles enable the rule-based layers.
type Config struct {
	KnownValues bool
	Secrets     bool
	IPs         bool
	Accounts    bool
	Hosts       bool
	Emails      bool
	Custom      bool

	Known         []Known
	CustomStrings []string
	ExcludedUsers map[string]bool
}

// Replacement is one value replaced (outbound) or restored (inbound).
type Replacement struct {
	Category Category
	Token    string
	Original string
}

// Guard owns the token mapping of one assistant run.
type Guard struct {
	cfg     Config
	byValue map[string]Replacement
	byToken map[string]string
	counts  map[Category]int
	custom  []string
}

// NewGuard builds a guard. The caller may reuse it for every model turn of a
// run: tokens stay stable because a value is only assigned a number once.
func NewGuard(cfg Config) *Guard {
	if cfg.ExcludedUsers == nil {
		cfg.ExcludedUsers = DefaultExcludedUsers
	}
	guard := &Guard{
		cfg:     cfg,
		byValue: make(map[string]Replacement),
		byToken: make(map[string]string),
		counts:  make(map[Category]int),
	}
	// Longest first: "Alice Smith" must win over "Alice".
	guard.custom = append([]string(nil), cfg.CustomStrings...)
	sort.SliceStable(guard.custom, func(i, j int) bool {
		return len(guard.custom[i]) > len(guard.custom[j])
	})
	return guard
}

// Token returns the token already assigned to value, if any. The assistant
// view uses it to render the audit details.
func (g *Guard) Token(value string) (Replacement, bool) {
	if g == nil {
		return Replacement{}, false
	}
	replacement, ok := g.byValue[value]
	return replacement, ok
}

// Protect replaces every detected value in text. The returned replacements
// describe what changed, deduplicated by token, in first-appearance order.
func (g *Guard) Protect(text string) (string, []Replacement) {
	if g == nil || text == "" {
		return text, nil
	}
	var replacements []Replacement
	// Keys and certificates go first: a block is replaced whole, so later rules
	// can never leave a fragment of one behind.
	if g.cfg.Secrets {
		text = protectPEM(text, &replacements, g)
	}
	if g.cfg.Custom && len(g.custom) > 0 {
		text = g.protectCustom(text, &replacements)
	}
	if g.cfg.KnownValues {
		text = g.protectKnown(text, &replacements)
	}
	if g.cfg.Secrets {
		text = protectSecrets(text, &replacements, g)
		text = protectSSHKeys(text, &replacements, g)
	}
	if g.cfg.Emails {
		text = protectEmails(text, &replacements, g)
	}
	// Accounts before addresses: in user@host the user is matched first, and a
	// tokenized address still sits where the host would be.
	if g.cfg.Accounts {
		text = protectAccounts(text, &replacements, g)
	}
	if g.cfg.IPs {
		text = protectIPs(text, &replacements, g)
	}
	if g.cfg.Hosts {
		text = protectDomains(text, &replacements, g)
	}
	return text, replacements
}

// Restore puts the original values back. It is the inverse of Protect for one
// complete string; streaming callers use NewStreamRestorer instead.
func (g *Guard) Restore(text string) (string, []Replacement) {
	if g == nil || text == "" {
		return text, nil
	}
	restorer := g.NewStreamRestorer()
	out := restorer.Write(text) + restorer.Flush()
	return out, restorer.Replacements()
}

// ProtectJSON tokenizes the string values inside a JSON document, such as a
// tool call's arguments. It falls back to plain text when the document is not
// valid JSON.
func (g *Guard) ProtectJSON(raw string) (string, []Replacement) {
	return transformJSON(raw, g.Protect)
}

// RestoreJSON restores a JSON document. Values restored inside string literals
// are escaped by the JSON encoder, so a password containing a quote cannot
// break a command that the model built around the token.
func (g *Guard) RestoreJSON(raw string) (string, []Replacement) {
	return transformJSON(raw, g.Restore)
}

func transformJSON(raw string, transform func(string) (string, []Replacement)) (string, []Replacement) {
	if raw == "" {
		return raw, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return transform(raw)
	}
	var replacements []Replacement
	changed := false
	var walk func(any) any
	walk = func(value any) any {
		switch typed := value.(type) {
		case string:
			out, found := transform(typed)
			if out != typed {
				changed = true
			}
			for _, replacement := range found {
				replacements = appendReplacement(replacements, replacement)
			}
			return out
		case []any:
			for index := range typed {
				typed[index] = walk(typed[index])
			}
			return typed
		case map[string]any:
			for key, item := range typed {
				typed[key] = walk(item)
			}
			return typed
		default:
			return value
		}
	}
	document = walk(document)
	if !changed {
		return raw, replacements
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return transform(raw)
	}
	return string(encoded), replacements
}

func (g *Guard) tokenFor(category Category, value string) Replacement {
	if existing, ok := g.byValue[value]; ok {
		return existing
	}
	g.counts[category]++
	token := TokenOpen + string(category) + "_" + strconv.Itoa(g.counts[category]) + TokenClose
	replacement := Replacement{Category: category, Token: token, Original: value}
	g.byValue[value] = replacement
	g.byToken[token] = value
	return replacement
}

func (g *Guard) protectCustom(text string, replacements *[]Replacement) string {
	for _, value := range g.custom {
		if value == "" {
			continue
		}
		text = replaceLiteral(text, value, false, func() Replacement {
			return g.tokenFor(CategoryCustom, value)
		}, replacements)
	}
	return text
}

func (g *Guard) protectKnown(text string, replacements *[]Replacement) string {
	known := append([]Known(nil), g.cfg.Known...)
	// Longest value first, so a label that contains the address wins over the
	// address alone.
	sort.SliceStable(known, func(i, j int) bool {
		return len(known[i].Value) > len(known[j].Value)
	})
	seen := make(map[string]bool, len(known))
	for _, item := range known {
		if item.Value == "" || seen[item.Value] {
			continue
		}
		seen[item.Value] = true
		value := item.Value
		category := item.Category
		text = replaceLiteral(text, value, item.Boundary, func() Replacement {
			return g.tokenFor(category, value)
		}, replacements)
	}
	return text
}

// replaceLiteral swaps every occurrence of value. When boundary is set, only
// matches surrounded by non-name bytes count, so "ann" does not hit "anna".
func replaceLiteral(text, value string, boundary bool, makeReplacement func() Replacement, replacements *[]Replacement) string {
	if value == "" {
		return text
	}
	found := false
	for start := 0; start <= len(text); {
		index := strings.Index(text[start:], value)
		if index < 0 {
			break
		}
		index += start
		if !boundary || hasNameBoundaries(text, index, index+len(value)) {
			found = true
			break
		}
		start = index + len(value)
	}
	if !found {
		return text
	}
	replacement := makeReplacement()
	*replacements = appendReplacement(*replacements, replacement)

	var out strings.Builder
	out.Grow(len(text))
	last := 0
	for start := 0; start <= len(text); {
		index := strings.Index(text[start:], value)
		if index < 0 {
			break
		}
		index += start
		if boundary && !hasNameBoundaries(text, index, index+len(value)) {
			start = index + len(value)
			continue
		}
		out.WriteString(text[last:index])
		out.WriteString(replacement.Token)
		last = index + len(value)
		start = last
	}
	out.WriteString(text[last:])
	return out.String()
}

type span struct {
	start int
	end   int
}

// tokenSpans locates the tokens already present in text. Every later detector
// skips a match that falls inside one, so a token is never replaced by a token
// of itself and restoration stays one pass.
func tokenSpans(text string) []span {
	var spans []span
	offset := 0
	for offset < len(text) {
		start := strings.Index(text[offset:], TokenOpen)
		if start < 0 {
			break
		}
		start += offset
		end := strings.Index(text[start:], TokenClose)
		if end < 0 {
			break
		}
		end += start + len(TokenClose)
		spans = append(spans, span{start: start, end: end})
		offset = end
	}
	return spans
}

func overlapsToken(spans []span, start, end int) bool {
	for _, item := range spans {
		if start < item.end && end > item.start {
			return true
		}
	}
	return false
}

// appendReplacement keeps the list in first-appearance order and free of the
// duplicate rows a value repeated in one message would otherwise create.
func appendReplacement(list []Replacement, replacement Replacement) []Replacement {
	for _, existing := range list {
		if existing.Token == replacement.Token {
			return list
		}
	}
	return append(list, replacement)
}

// Mask hides the value in an audit row. Passwords, keys and certificates are
// always masked; addresses, users and names are shown in full because the
// audit exists to let the user verify them.
func Mask(category Category, value string) string {
	switch category {
	case CategorySecret, CategoryKey, CategoryCert:
		return maskSecret(value)
	default:
		return value
	}
}

func maskSecret(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 4 {
		return "••••"
	}
	return string(runes[:2]) + "••••" + string(runes[len(runes)-2:])
}

// Preview shortens a message for the audit list, keeping at most limit runes.
func Preview(text string, limit int) string {
	if limit <= 0 {
		limit = 160
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// nameByte is the character class of a user, host or path segment. A known
// value only matches when the bytes around it are outside this class.
func nameByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_', b == '.', b == '-':
		return true
	default:
		return false
	}
}

func hasNameBoundaries(text string, start, end int) bool {
	return (start == 0 || !nameByte(text[start-1])) && (end >= len(text) || !nameByte(text[end]))
}
