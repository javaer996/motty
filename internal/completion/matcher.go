package completion

import (
	"strings"
	"unicode"
)

// MaxCandidates bounds one answer. The list shows fewer rows than this; the
// bound exists so a broad prefix cannot build an unbounded response.
const MaxCandidates = 100

const (
	maxLineBytes = 4096
	maxTokens    = 128
)

// Kind tells the caller what a candidate is. The list uses it for ordering and
// the ghost suggestion only accepts a unique command, option or value.
type Kind uint8

const (
	KindCommand Kind = iota
	KindOption
	KindValue
)

// Candidate is one possible continuation. Text is the complete token; Insert is
// the suffix that is appended to the current word, which is never rewritten.
type Candidate struct {
	Text   string
	Insert string
	Desc   string
	Kind   Kind
	// FromCatalog marks text built from the command catalog. The workbench
	// revalidates exactly those insertions before typing them; suggestions
	// learned from the user's own history are not restricted this way.
	FromCatalog bool
}

// Result is the answer for one line. Suppressed reports syntax this package
// does not understand: the caller must leave the input alone and close any
// list, because no suffix can be proven safe.
type Result struct {
	Prefix     string
	Word       string
	Candidates []Candidate
	Suppressed bool
}

// Complete answers what the end of line could continue into. The line is
// expected to be a single command line with the cursor at its end; anything
// else is treated as unsupported rather than guessed at.
func Complete(catalog *Catalog, line string) Result {
	if catalog == nil {
		return Result{Suppressed: true}
	}
	if len(line) > maxLineBytes || strings.ContainsAny(line, "\t\r\n") {
		return Result{Suppressed: true}
	}
	for _, r := range line {
		if !lineRune(r) {
			return Result{Suppressed: true}
		}
	}
	if strings.HasPrefix(line, " ") || strings.Contains(line, "  ") {
		// A leading space hides the command from shell history, and an empty
		// token makes the word boundaries ambiguous.
		return Result{Suppressed: true}
	}

	var prefix, word string
	if strings.HasSuffix(line, " ") {
		prefix, word = line, ""
	} else if index := strings.LastIndexByte(line, ' '); index >= 0 {
		prefix, word = line[:index+1], line[index+1:]
	} else {
		word = line
	}

	result := Result{Prefix: prefix, Word: word}
	var tokens []string
	if trimmed := strings.TrimSuffix(prefix, " "); trimmed != "" {
		tokens = strings.Split(trimmed, " ")
		if len(tokens) > maxTokens {
			return Result{Suppressed: true}
		}
	}
	if len(tokens) == 0 {
		if word == "" {
			addRoots(catalog, "", &result)
		} else {
			addRoots(catalog, word, &result)
		}
		return result
	}

	root := catalog.Root(tokens[0])
	if root == nil {
		// An unknown first word may still be a local script; the static catalog
		// has nothing to say, and history suggestions are handled by the caller.
		return result
	}

	node := root
	var expect *Flag
	positional := false
	flagsStopped := false
	used := make(map[string]bool)
	for _, token := range tokens[1:] {
		if expect != nil {
			expect = nil
			continue
		}
		if !flagsStopped {
			if token == "--" {
				flagsStopped = true
				continue
			}
			if strings.HasPrefix(token, "--") {
				name, _, hasValue := strings.Cut(token, "=")
				flag, ok := catalog.FindFlag(node, name)
				if !ok {
					return Result{Suppressed: true}
				}
				used[flag.Token()] = true
				if !hasValue && flag.Value && !flag.Optional {
					expect = &flag
				}
				continue
			}
			if strings.HasPrefix(token, "-") && len(token) > 1 {
				// A multi-character option may be defined as one short name
				// (-br, -sS) rather than a cluster of single letters.
				if flag, ok := catalog.FindFlag(node, token); ok {
					used[flag.Token()] = true
					if flag.Value && !flag.Optional {
						expect = &flag
					}
					continue
				}
				// Otherwise it is a cluster of short options, as in -tuln or
				// -lah. Every letter has to be a known option that does not
				// consume a value; a value-taking letter ends the cluster and
				// may carry its value in the rest of the word (-p22).
				if !parseShortCluster(catalog, node, token, used, &expect) {
					return Result{Suppressed: true}
				}
				continue
			}
		}
		if flagsStopped || positional || token == "-" {
			positional = true
			continue
		}
		if child := childByToken(node, token); child != nil {
			node = child
			continue
		}
		positional = true
	}
	if expect != nil {
		addValues(expect, word, &result)
		return result
	}
	if flagsStopped {
		return result
	}
	if word == "" {
		if positional {
			return result
		}
		addChildren(node, "", &result)
		addLongFlags(catalog, node, "", used, &result)
		return result
	}
	if strings.HasPrefix(word, "--") {
		if name, value, hasValue := strings.Cut(word, "="); hasValue {
			flag, ok := catalog.FindFlag(node, name)
			if !ok || !flag.Value {
				return result
			}
			addValues(&flag, value, &result)
			return result
		}
		addLongFlags(catalog, node, word, used, &result)
		return result
	}
	if strings.HasPrefix(word, "-") {
		if len(word) > 2 {
			// A short-option cluster is complete syntax of its own; do not guess.
			return result
		}
		addShortFlags(catalog, node, word, used, &result)
		return result
	}
	if positional {
		return result
	}
	addChildren(node, word, &result)
	return result
}

// lineRune accepts only characters that cannot change the meaning of the line
// for a shell or for this tokenizer. Quotes, expansions, redirects, globs and
// command separators are all refused.
func lineRune(r rune) bool {
	switch {
	case r >= 0x80:
		return true
	case unicode.IsLetter(r) || unicode.IsDigit(r):
		return true
	}
	return r == ' ' || strings.ContainsRune("-_.:@%+,=/", r)
}

// parseShortCluster marks every short option in token as used. A letter that
// takes a value ends the cluster: the rest of the word is its value, or, when
// the letter is last, the next word is. Unknown letters make the line
// unsupported rather than guessed at.
func parseShortCluster(catalog *Catalog, node *Node, token string, used map[string]bool, expect **Flag) bool {
	letters := []rune(token[1:])
	for index, letter := range letters {
		flag, ok := catalog.FindFlag(node, "-"+string(letter))
		if !ok {
			return false
		}
		used[flag.Token()] = true
		if !flag.Value {
			continue
		}
		if index == len(letters)-1 && !flag.Optional {
			*expect = &flag
		}
		return true
	}
	return true
}

func childByToken(node *Node, token string) *Node {
	node.load()
	for _, child := range node.Children {
		if child.Token == token {
			return child
		}
		for _, alias := range child.Aliases {
			if alias == token {
				return child
			}
		}
	}
	return nil
}

func addRoots(catalog *Catalog, prefix string, result *Result) {
	for _, root := range catalog.Roots() {
		addCandidate(result, root.Token, prefix, root.Desc, KindCommand)
		for _, alias := range root.Aliases {
			addCandidate(result, alias, prefix, root.Desc, KindCommand)
		}
	}
}

func addChildren(node *Node, prefix string, result *Result) {
	node.load()
	for _, child := range node.Children {
		if child.Hidden {
			continue
		}
		addCandidate(result, child.Token, prefix, child.Desc, KindCommand)
		for _, alias := range child.Aliases {
			addCandidate(result, alias, prefix, child.Desc, KindCommand)
		}
	}
}

func addLongFlags(catalog *Catalog, node *Node, prefix string, used map[string]bool, result *Result) {
	for _, flag := range catalog.visibleFlagsInOrder(node) {
		if flag.Long == "" {
			continue
		}
		if used[flag.Token()] && !flag.Repeatable {
			continue
		}
		addCandidate(result, flag.Token(), prefix, flag.Desc, KindOption)
	}
}

func addShortFlags(catalog *Catalog, node *Node, prefix string, used map[string]bool, result *Result) {
	for _, flag := range catalog.visibleFlagsInOrder(node) {
		if flag.Short == "" {
			continue
		}
		if used[flag.Token()] && !flag.Repeatable {
			continue
		}
		addCandidate(result, flag.ShortToken(), prefix, flag.Desc, KindOption)
	}
}

func addValues(flag *Flag, prefix string, result *Result) {
	for _, value := range flag.Values {
		if !strings.HasPrefix(value.Text, prefix) {
			continue
		}
		addCandidate(result, value.Text, prefix, value.Desc, KindValue)
	}
}

func addCandidate(result *Result, text, prefix, desc string, kind Kind) {
	if len(result.Candidates) >= MaxCandidates {
		return
	}
	if !strings.HasPrefix(text, prefix) || text == prefix {
		return
	}
	duplicate := false
	for _, existing := range result.Candidates {
		if existing.Text == text && existing.Kind == kind {
			duplicate = true
			break
		}
	}
	if duplicate {
		return
	}
	result.Candidates = append(result.Candidates, Candidate{
		Text:        text,
		Insert:      text[len(prefix):],
		Desc:        desc,
		Kind:        kind,
		FromCatalog: true,
	})
}
