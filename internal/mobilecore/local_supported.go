//go:build !ios

package mobilecore

import (
	"os"
	"path/filepath"
	"runtime"
	"time"

	"motty/internal/localshell"
	"motty/internal/sessionlog"
	"motty/internal/sshconn"
)

// localSizeGrace is how long a new local session waits for the front end to
// report the terminal's measured size before starting the shell with the
// default one. The Android view sends its size within one layout pass.
const localSizeGrace = time.Second

// openLocal starts a shell on this device in a new tab. Android is the only
// front end that enables it; the iOS stub below reports unavailable.
func (m *sessionManager) openLocal(cols, rows int) (SessionInfo, error) {
	s := m.startSession(Host{Label: "Local Shell"}, cols, rows, true)
	m.logBegin(s)
	go s.connectLocal()
	return s.info(), nil
}

// connectLocal starts the shell and wires it into the same pump the SSH
// sessions use.
func (s *session) connectLocal() {
	s.mu.Lock()
	run := s.run
	first := s.connectedAt.IsZero()
	cols, rows := s.cols, s.rows
	s.mu.Unlock()

	if first && runtime.GOOS == "android" {
		// The Android view reports its measured size right after it is laid
		// out. Start the shell only after that: mksh erases its prompt line
		// on a width change and does not draw it again, so a shell started at
		// the default 80x24 loses its prompt on the first resize. Other
		// platforms (the macOS test build) start right away.
		timer := time.NewTimer(localSizeGrace)
		select {
		case <-run.done:
			timer.Stop()
			return
		case <-s.sizeHint:
			timer.Stop()
		case <-timer.C:
		}
		s.mu.Lock()
		cols, rows = s.cols, s.rows
		s.mu.Unlock()
	}

	home := filepath.Join(s.manager.dataDir, "home")
	tmp := filepath.Join(s.manager.dataDir, "tmp")
	program := localShellProgram()
	shell, err := localshell.Start(localshell.Config{
		Cols:  cols,
		Rows:  rows,
		Shell: program,
		// An empty (not nil) list keeps localshell from adding -l: Android's
		// mksh should start as an ordinary interactive shell in our home.
		Args:   []string{},
		Dir:    home,
		Env:    localEnv(program, home, tmp),
		Notify: s.outputWakeup,
	})
	if err != nil {
		s.failLocal(run, err)
		return
	}

	s.mu.Lock()
	s.phase = sshconn.PhaseConnected
	s.connectedAt = time.Now()
	s.shell = shell
	liveCols, liveRows := s.cols, s.rows
	outputWake := s.outputWake
	s.mu.Unlock()
	if liveCols != cols || liveRows != rows {
		// A resize landed while the shell was starting; the pty was created
		// with the old size.
		shell.Resize(liveCols, liveRows)
	}

	s.manager.logConnected(s.id)
	s.emitState()
	go s.pump(shell, run.done, outputWake)
	go s.watchLocal(shell, run)
}

// watchLocal reports the end of the local process. The tab stays readable
// with its last screen and can reconnect, exactly like a dropped SSH session.
func (s *session) watchLocal(shell *localshell.Shell, run *sessionRun) {
	select {
	case <-run.done:
		return
	case <-shell.Done():
	}
	err := shell.Err()
	s.mu.Lock()
	if s.shell == shell {
		s.shell = nil
	}
	s.err = err
	s.phase = sshconn.PhaseClosed
	s.mu.Unlock()
	if err != nil {
		s.manager.logFinish(s.id, sessionlog.StatusFailed, sessionlog.ReasonShellFailed)
	} else {
		s.manager.logFinish(s.id, sessionlog.StatusEnded, sessionlog.ReasonUserClosed)
	}
	s.emitState()
	run.markDone()
}

func (s *session) failLocal(run *sessionRun, err error) {
	s.mu.Lock()
	s.err = err
	s.phase = sshconn.PhaseFailed
	s.mu.Unlock()
	s.manager.logFinish(s.id, sessionlog.StatusFailed, sessionlog.ReasonShellFailed)
	s.emitState()
	run.markDone()
}

// localShellProgram is the shell Android runs. /system/bin/sh is mksh on the
// target device, but it is the system shell either way; desktop test builds
// use /bin/sh so a developer's interactive shell configuration cannot leak
// into the tests.
func localShellProgram() string {
	if runtime.GOOS == "android" {
		return "/system/bin/sh"
	}
	if _, err := os.Stat("/bin/sh"); err == nil {
		return "/bin/sh"
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// localEnv is appended to the inherited environment; later entries win.
// Android's process starts with a zygote environment, so the shell gets an
// explicit home, temp directory and PATH instead of inheriting /sdcard.
func localEnv(program, home, tmp string) []string {
	env := []string{
		"HOME=" + home,
		"TMPDIR=" + tmp,
		"SHELL=" + program,
		"LANG=C.UTF-8",
	}
	if runtime.GOOS == "android" {
		env = append(env,
			"PATH=/system/bin:/system/xbin:/vendor/bin:/product/bin",
			"USER=android",
			"LOGNAME=android",
		)
	} else if path := os.Getenv("PATH"); path != "" {
		env = append(env, "PATH="+path)
	}
	return env
}
