package privacy

import "strings"

// StreamRestorer restores tokens in text that arrives in pieces, as an SSE
// answer does. A token split across two chunks is held back until its closing
// bracket arrives, so the reader never sees half a token and the original
// value is never cut in two.
type StreamRestorer struct {
	guard    *Guard
	pending  string
	hits     []Replacement
	maxToken int
}

// NewStreamRestorer starts one streamed answer.
func (g *Guard) NewStreamRestorer() *StreamRestorer {
	restorer := &StreamRestorer{guard: g}
	if g != nil {
		for token := range g.byToken {
			if len(token) > restorer.maxToken {
				restorer.maxToken = len(token)
			}
		}
	}
	return restorer
}

// Write consumes one text delta and returns everything that is safe to show.
// The tail that could still be the beginning of a token stays pending.
func (s *StreamRestorer) Write(chunk string) string {
	if s == nil || s.guard == nil {
		return chunk
	}
	s.pending += chunk
	var out strings.Builder
	for {
		index := strings.Index(s.pending, TokenOpen)
		if index < 0 {
			// A chunk boundary can fall inside the multi-byte opener. Hold back
			// the longest suffix that could still become one.
			hold := partialOpenerSuffix(s.pending)
			out.WriteString(s.pending[:len(s.pending)-hold])
			s.pending = s.pending[len(s.pending)-hold:]
			return out.String()
		}
		out.WriteString(s.pending[:index])
		rest := s.pending[index:]
		end := strings.Index(rest, TokenClose)
		if end < 0 {
			// An unterminated opener longer than any real token is text, not a
			// token: let it through rather than buffering without bound.
			if s.maxToken > 0 && len(rest) > s.maxToken {
				out.WriteString(TokenOpen)
				s.pending = rest[len(TokenOpen):]
				continue
			}
			s.pending = rest
			return out.String()
		}
		token := rest[:end+len(TokenClose)]
		if value, ok := s.guard.byToken[token]; ok {
			out.WriteString(value)
			s.hits = appendReplacement(s.hits, Replacement{
				Category: s.guard.byValue[value].Category,
				Token:    token,
				Original: value,
			})
		} else {
			// The model invented a bracket sequence; it is ordinary text.
			out.WriteString(token)
		}
		s.pending = rest[end+len(TokenClose):]
	}
}

// partialOpenerSuffix is the length of the longest suffix of text that is a
// proper prefix of TokenOpen. Those bytes must wait for the next chunk.
func partialOpenerSuffix(text string) int {
	for length := len(TokenOpen) - 1; length > 0; length-- {
		if length > len(text) {
			continue
		}
		if strings.HasPrefix(TokenOpen, text[len(text)-length:]) {
			return length
		}
	}
	return 0
}

// Flush returns whatever is still held back at the end of the stream.
func (s *StreamRestorer) Flush() string {
	if s == nil {
		return ""
	}
	out := s.pending
	s.pending = ""
	return out
}

// Replacements lists the tokens this stream restored, in order.
func (s *StreamRestorer) Replacements() []Replacement {
	if s == nil {
		return nil
	}
	return append([]Replacement(nil), s.hits...)
}
