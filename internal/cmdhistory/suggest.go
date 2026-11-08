package cmdhistory

import (
	"sort"
	"strings"
	"time"
)

// Suggest is the rest of the command that the line typed so far most likely
// continues into: the most recently used entry that starts with it and is
// longer. entries are newest first, as Store.List returns them. A line that
// reads like a secret, or ends in a newline, gets nothing.
func Suggest(entries []Entry, line string) (rest string, ok bool) {
	matches := SuggestAll(entries, line, 1)
	if len(matches) == 0 {
		return "", false
	}
	return matches[0], true
}

// SuggestAll returns up to maxMatches command suffixes that match the line.
// Matches are sorted by recency (entries are newest first). A line that
// reads like a secret, or ends in a newline, gets nothing.
func SuggestAll(entries []Entry, line string, maxMatches int) []string {
	if line == "" || strings.ContainsAny(line, "\r\n") || Secret(line) || maxMatches <= 0 {
		return nil
	}
	var matches []string
	for _, entry := range entries {
		if len(entry.Command) > len(line) && strings.HasPrefix(entry.Command, line) {
			matches = append(matches, entry.Command[len(line):])
			if len(matches) >= maxMatches {
				break
			}
		}
	}
	return matches
}

// SuggestCommands returns up to maxMatches completions for the command word
// itself. Only the first token of each remembered command is considered, so
// typing a prefix never inserts arguments before the command is finished.
// Matches are ranked like SuggestRanked.
func SuggestCommands(entries []Entry, prefix string, maxMatches int) []string {
	if prefix == "" || strings.ContainsAny(prefix, " \t\r\n") || Secret(prefix) || maxMatches <= 0 {
		return nil
	}
	type match struct {
		rest  string
		count int
		used  time.Time
	}
	seen := make(map[string]int, maxMatches)
	matches := make([]match, 0, maxMatches)
	for _, entry := range entries {
		command := entry.Command
		if index := strings.IndexAny(command, " \t"); index >= 0 {
			command = command[:index]
		}
		if len(command) <= len(prefix) || !strings.HasPrefix(command, prefix) {
			continue
		}
		rest := command[len(prefix):]
		if index, ok := seen[rest]; ok {
			// The same command word reached through different argument lines:
			// keep the strongest use so ranking stays meaningful.
			if entry.Count > matches[index].count ||
				(entry.Count == matches[index].count && entry.Used.After(matches[index].used)) {
				matches[index].count = entry.Count
				matches[index].used = entry.Used
			}
			continue
		}
		seen[rest] = len(matches)
		matches = append(matches, match{rest: rest, count: entry.Count, used: entry.Used})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].count != matches[j].count {
			return matches[i].count > matches[j].count
		}
		return matches[i].used.After(matches[j].used)
	})
	out := make([]string, 0, maxMatches)
	for _, match := range matches {
		out = append(out, match.rest)
		if len(out) >= maxMatches {
			break
		}
	}
	return out
}

// SuggestRanked is SuggestAll ordered by how often the command was used, then
// by recency. A command typed once today is a weaker hint than one typed a
// hundred times, which is what makes the list worth reading after two letters.
func SuggestRanked(entries []Entry, line string, maxMatches int) []string {
	if line == "" || strings.ContainsAny(line, "\r\n") || Secret(line) || maxMatches <= 0 {
		return nil
	}
	type match struct {
		rest  string
		count int
		used  time.Time
	}
	matches := make([]match, 0, maxMatches)
	for _, entry := range entries {
		if len(entry.Command) > len(line) && strings.HasPrefix(entry.Command, line) {
			matches = append(matches, match{
				rest:  entry.Command[len(line):],
				count: entry.Count,
				used:  entry.Used,
			})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].count != matches[j].count {
			return matches[i].count > matches[j].count
		}
		return matches[i].used.After(matches[j].used)
	})
	out := make([]string, 0, maxMatches)
	for _, match := range matches {
		out = append(out, match.rest)
		if len(out) >= maxMatches {
			break
		}
	}
	return out
}
