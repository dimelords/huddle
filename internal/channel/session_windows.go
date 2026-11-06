//go:build windows

package channel

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx = kernel32.NewProc("LockFileEx")
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	// PROCESS_QUERY_LIMITED_INFORMATION is enough for GetProcessTimes and,
	// unlike PROCESS_QUERY_INFORMATION, is granted for elevated processes.
	processQueryLimitedInformation = 0x1000
	// How far up the process tree to watch: Claude Code, Git Bash and the
	// hook script are a handful of levels; the rest is the terminal.
	maxAncestors = 16
)

// tryLock takes an exclusive lock on f without waiting, and fails when
// another process holds it. Windows releases it when the handle is closed or
// the process exits, like flock.
func tryLock(f *os.File) error {
	var ol syscall.Overlapped
	r, _, err := procLockFileEx.Call(
		f.Fd(),
		lockfileExclusiveLock|lockfileFailImmediately,
		0, 1, 0,
		uintptr(unsafe.Pointer(&ol)),
	)
	if r == 0 {
		return err
	}
	return nil
}

// watchSession reports whether the Claude Code session that started the hook
// is still there. Windows does not reparent orphans, so the parent's pid never
// changes; and Git Bash keeps a stub process alive while the program it ran
// with exec is running, so the direct parent outlives the session. Instead we
// hold a handle to every ancestor and stop as soon as one of them exits.
func watchSession() (alive func() bool, release func()) {
	handles := ancestors()
	alive = func() bool {
		for _, h := range handles {
			if ev, err := syscall.WaitForSingleObject(h, 0); err != nil || ev != syscall.WAIT_TIMEOUT {
				return false
			}
		}
		return true
	}
	release = func() {
		for _, h := range handles {
			syscall.CloseHandle(h)
		}
	}
	return alive, release
}

// ancestors opens a handle to each process above this one. The walk stops at
// a process it may not open, or at one that started after its child: the
// parent pid is only recorded at creation, so a younger process with that pid
// is a stranger that reused it.
func ancestors() []syscall.Handle {
	parents := parentPids()
	var handles []syscall.Handle
	pid := uint32(os.Getpid())
	born := creationTime(syscall.Handle(0))
	for len(handles) < maxAncestors {
		ppid, ok := parents[pid]
		if !ok || ppid == 0 || ppid == pid {
			break
		}
		h, err := syscall.OpenProcess(syscall.SYNCHRONIZE|processQueryLimitedInformation, false, ppid)
		if err != nil {
			break
		}
		t := creationTime(h)
		if t == 0 || (born != 0 && t > born) {
			syscall.CloseHandle(h)
			break
		}
		handles = append(handles, h)
		pid, born = ppid, t
	}
	return handles
}

// parentPids maps every running process to its parent.
func parentPids() map[uint32]uint32 {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer syscall.CloseHandle(snap)
	m := make(map[uint32]uint32)
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = syscall.Process32First(snap, &e); err == nil; err = syscall.Process32Next(snap, &e) {
		m[e.ProcessID] = e.ParentProcessID
	}
	return m
}

// creationTime returns when a process started, in 100 ns ticks, or 0 when it
// cannot be read. With a zero handle it reads the current process.
func creationTime(h syscall.Handle) int64 {
	if h == 0 {
		cur, err := syscall.GetCurrentProcess()
		if err != nil {
			return 0
		}
		h = cur
	}
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0
	}
	return created.Nanoseconds() / 100
}
