package mobilecore

import (
	"strings"

	"motty/internal/otp"
	"motty/internal/sshconn"
)

// The parts of a host's secrets: hostSecretName(catalogID, part). They use the
// desktop's names so the same password reaches both versions through a backup
// or a sync.
const (
	hostPasswordPart = "password"
	hostOTPPart      = "otp"
)

func hostSecretName(catalogID, part string) string {
	if catalogID == "" {
		return ""
	}
	return "host:" + catalogID + ":" + part
}

func passphraseSecretName(identity string) string {
	if identity == "" {
		return ""
	}
	return "passphrase:" + identity
}

// isPasswordQuestion reports a single secret the server names as a password.
// A one-time code or an unknown secret question is not one: filling a saved
// password into it would only be refused.
func isPasswordQuestion(questions []sshconn.Question) bool {
	if len(questions) != 1 || !questions[0].Secret {
		return false
	}
	wording := questions[0].Text
	return wording == "" || !otp.LooksLikeQuestion(wording) && looksLikePasswordPrompt(wording)
}

// looksLikePasswordPrompt names the password in the languages servers commonly
// answer in, as the desktop version does.
func looksLikePasswordPrompt(wording string) bool {
	lower := strings.ToLower(wording)
	for _, word := range []string{
		"password", "passwd", "passwort", "mot de passe", "contraseña", "senha", "пароль",
		"密码", "密碼", "口令", "パスワード", "비밀번호",
	} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// answerSavedSecret answers a first-attempt prompt from the vault, so a
// password or passphrase saved here (or arriving through a backup or a sync)
// needs no typing. A refused answer comes back as a second attempt, which is
// always left to the user.
func (s *session) answerSavedSecret(kind string, status sshconn.Status) bool {
	if status.Attempt > 1 || s.manager == nil || s.manager.data == nil {
		return false
	}
	var name string
	switch kind {
	case "password":
		if s.host.ID == "" || !isPasswordQuestion(status.Questions) {
			return false
		}
		name = hostSecretName(s.host.ID, hostPasswordPart)
	case "passphrase":
		name = passphraseSecretName(status.IdentityPath)
	default:
		return false
	}
	if name == "" {
		return false
	}
	value, found, err := s.manager.data.secretGet(name)
	if err != nil || !found || value == "" {
		return false
	}
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return false
	}
	switch kind {
	case "password":
		conn.SubmitPassword(value)
	case "passphrase":
		conn.SubmitKeyPassphrase(value)
	}
	s.mu.Lock()
	s.filledSecret = name
	s.mu.Unlock()
	return true
}

// forgetRefusedSecret drops a saved secret the server has just refused, so the
// next connection asks the user instead of repeating the stale answer.
func (s *session) forgetRefusedSecret() {
	s.mu.Lock()
	name := s.filledSecret
	s.filledSecret = ""
	s.mu.Unlock()
	if name == "" || s.manager == nil || s.manager.data == nil {
		return
	}
	if err := s.manager.data.secretDelete(name); err != nil {
		return
	}
	if s.manager.changed != nil {
		s.manager.changed()
	}
}

// stagePasswordSave remembers the password the user asked to save. It is kept
// only until the connection is established: a password the server refuses is
// never written.
func (s *session) stagePasswordSave(value string) {
	if value == "" || s.host.ID == "" {
		return
	}
	s.mu.Lock()
	questions := append([]sshconn.Question(nil), s.promptQuestions...)
	s.mu.Unlock()
	if !isPasswordQuestion(questions) {
		return
	}
	s.mu.Lock()
	s.pendingSecretName = hostSecretName(s.host.ID, hostPasswordPart)
	s.pendingSecretValue = value
	s.mu.Unlock()
}

// commitPendingSecret writes the staged password once the server accepted the
// login.
func (s *session) commitPendingSecret() {
	s.mu.Lock()
	name, value := s.pendingSecretName, s.pendingSecretValue
	s.pendingSecretName, s.pendingSecretValue = "", ""
	s.mu.Unlock()
	if name == "" || value == "" || s.manager == nil || s.manager.data == nil {
		return
	}
	if err := s.manager.data.secretSet(name, value); err != nil {
		return
	}
	if s.manager.changed != nil {
		s.manager.changed()
	}
}

func (s *session) discardPendingSecret() {
	s.mu.Lock()
	s.pendingSecretName, s.pendingSecretValue = "", ""
	s.mu.Unlock()
}
