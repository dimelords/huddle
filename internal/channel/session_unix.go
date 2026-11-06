//go:build !windows

package channel

import (
	"os"
	"syscall"
)

// tryLock takes an exclusive lock on f without waiting, and fails when
// another process holds it.
func tryLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// watchSession reports whether the Claude Code session that started the hook
// is still there. The hook's parent is the shell Claude Code spawned; when it
// goes away we are reparented, and nobody would be woken.
func watchSession() (alive func() bool, release func()) {
	parent := os.Getppid()
	return func() bool { return os.Getppid() == parent }, func() {}
}
