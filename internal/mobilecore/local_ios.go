//go:build ios

package mobilecore

// iOS forbids starting processes, so the local shell never exists there. The
// stub keeps internal/localshell (and creack/pty) out of the iOS archive.

func (m *sessionManager) openLocal(cols, rows int) (SessionInfo, error) {
	return SessionInfo{}, errorf(CodeUnavailable, "this platform does not support a local shell")
}

func (s *session) connectLocal() {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	s.fail(run, errorf(CodeUnavailable, "this platform does not support a local shell"))
}
