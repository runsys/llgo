// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux

package syscall

import (
	"runtime"
	"unsafe"

	c "github.com/goplus/llgo/runtime/internal/clite"
	"github.com/goplus/llgo/runtime/internal/clite/os"
	"github.com/goplus/llgo/runtime/internal/clite/syscall"
)

// Linux unshare/clone/clone2/clone3 flags, architecture-independent,
// copied from linux/sched.h.
const (
	CLONE_VM             = 0x00000100 // set if VM shared between processes
	CLONE_FS             = 0x00000200 // set if fs info shared between processes
	CLONE_FILES          = 0x00000400 // set if open files shared between processes
	CLONE_SIGHAND        = 0x00000800 // set if signal handlers and blocked signals shared
	CLONE_PIDFD          = 0x00001000 // set if a pidfd should be placed in parent
	CLONE_PTRACE         = 0x00002000 // set if we want to let tracing continue on the child too
	CLONE_VFORK          = 0x00004000 // set if the parent wants the child to wake it up on mm_release
	CLONE_PARENT         = 0x00008000 // set if we want to have the same parent as the cloner
	CLONE_THREAD         = 0x00010000 // Same thread group?
	CLONE_NEWNS          = 0x00020000 // New mount namespace group
	CLONE_SYSVSEM        = 0x00040000 // share system V SEM_UNDO semantics
	CLONE_SETTLS         = 0x00080000 // create a new TLS for the child
	CLONE_PARENT_SETTID  = 0x00100000 // set the TID in the parent
	CLONE_CHILD_CLEARTID = 0x00200000 // clear the TID in the child
	CLONE_DETACHED       = 0x00400000 // Unused, ignored
	CLONE_UNTRACED       = 0x00800000 // set if the tracing process can't force CLONE_PTRACE on this clone
	CLONE_CHILD_SETTID   = 0x01000000 // set the TID in the child
	CLONE_NEWCGROUP      = 0x02000000 // New cgroup namespace
	CLONE_NEWUTS         = 0x04000000 // New utsname namespace
	CLONE_NEWIPC         = 0x08000000 // New ipc namespace
	CLONE_NEWUSER        = 0x10000000 // New user namespace
	CLONE_NEWPID         = 0x20000000 // New pid namespace
	CLONE_NEWNET         = 0x40000000 // New network namespace
	CLONE_IO             = 0x80000000 // Clone io context

	// Flags for the clone3() syscall.

	CLONE_CLEAR_SIGHAND = 0x100000000 // Clear any signal handler and reset to SIG_DFL.
	CLONE_INTO_CGROUP   = 0x200000000 // Clone into a specific cgroup given the right permissions.

	// Cloning flags intersect with CSIGNAL so can be used with unshare and clone3
	// syscalls only:

	CLONE_NEWTIME = 0x00000080 // New time namespace
)

// SysProcIDMap holds Container ID to Host ID mappings used for User Namespaces in Linux.
// See user_namespaces(7).
type SysProcIDMap struct {
	ContainerID int // Container ID.
	HostID      int // Host ID.
	Size        int // Size.
}

type SysProcAttr struct {
	Chroot     string      // Chroot.
	Credential *Credential // Credential.
	// Ptrace tells the child to call ptrace(PTRACE_TRACEME).
	// Call runtime.LockOSThread before starting a process with this set,
	// and don't call UnlockOSThread until done with PtraceSyscall calls.
	Ptrace bool
	Setsid bool // Create session.
	// Setpgid sets the process group ID of the child to Pgid,
	// or, if Pgid == 0, to the new child's process ID.
	Setpgid bool
	// Setctty sets the controlling terminal of the child to
	// file descriptor Ctty. Ctty must be a descriptor number
	// in the child process: an index into ProcAttr.Files.
	// This is only meaningful if Setsid is true.
	Setctty bool
	Noctty  bool // Detach fd 0 from controlling terminal
	Ctty    int  // Controlling TTY fd
	// Foreground places the child process group in the foreground.
	// This implies Setpgid. The Ctty field must be set to
	// the descriptor of the controlling TTY.
	// Unlike Setctty, in this case Ctty must be a descriptor
	// number in the parent process.
	Foreground bool
	Pgid       int // Child's process group ID if Setpgid.
	// Pdeathsig, if non-zero, is a signal that the kernel will send to
	// the child process when the creating thread dies. Note that the signal
	// is sent on thread termination, which may happen before process termination.
	// There are more details at https://go.dev/issue/27505.
	Pdeathsig    Signal
	Cloneflags   uintptr        // Flags for clone calls (Linux only)
	Unshareflags uintptr        // Flags for unshare calls (Linux only)
	UidMappings  []SysProcIDMap // User ID mappings for user namespaces.
	GidMappings  []SysProcIDMap // Group ID mappings for user namespaces.
	// GidMappingsEnableSetgroups enabling setgroups syscall.
	// If false, then setgroups syscall will be disabled for the child process.
	// This parameter is no-op if GidMappings == nil. Otherwise for unprivileged
	// users this should be set to false for mappings work.
	GidMappingsEnableSetgroups bool
	AmbientCaps                []uintptr // Ambient capabilities (Linux only)
	UseCgroupFD                bool      // Whether to make use of the CgroupFD field.
	CgroupFD                   int       // File descriptor of a cgroup to put the new process into.
}

var (
	none  = [...]byte{'n', 'o', 'n', 'e', 0}
	slash = [...]byte{'/', 0}
)

/* TODO(xsw):
// Implemented in runtime package.
func runtime_BeforeFork()
func runtime_AfterFork()
func runtime_AfterForkInChild()
*/

// Fork, dup fd onto 0..len(fd), and exec(argv0, argvv, envv) in child.
// If a dup or exec fails, write the errno error to pipe.
// (Pipe is close-on-exec so if exec succeeds, it will be closed.)
// In the child, this function must not acquire any locks, because
// they might have been locked at the time of the fork. This means
// no rescheduling, no malloc calls, and no new stack segments.
// For the same reason compiler does not race instrument it.
// The calls to RawSyscall are okay because they are assembly
// functions that do not grow the stack.
//
// func forkAndExecInChild(argv0 *byte, argv, envv []*byte, chroot, dir *byte, attr *ProcAttr, sys *SysProcAttr, pipe int) (pid int, err Errno) {
func forkAndExecInChild(argv0 *c.Char, argv, envv **c.Char, chroot, dir *c.Char, attr *ProcAttr, sys *SysProcAttr, pipe int) (pid int, err Errno) {
	// Set up and fork. This returns immediately in the parent or
	// if there's an error.
	upid, err, _, _ := forkAndExecInChild1(argv0, argv, envv, chroot, dir, attr, sys, pipe)
	/**
	if locked {
		runtime_AfterFork()
	}
	*/
	if err != 0 {
		return 0, err
	}

	// parent; return PID
	pid = int(upid)

	if sys.UidMappings != nil || sys.GidMappings != nil {
		/**
		Close(mapPipe[0])
		var err2 Errno
		// uid/gid mappings will be written after fork and unshare(2) for user
		// namespaces.
		if sys.Unshareflags&CLONE_NEWUSER == 0 {
			if err := writeUidGidMappings(pid, sys); err != nil {
				err2 = err.(Errno)
			}
		}
		RawSyscall(SYS_WRITE, uintptr(mapPipe[1]), uintptr(unsafe.Pointer(&err2)), unsafe.Sizeof(err2))
		Close(mapPipe[1])
		*/
		panic("todo: syscall.forkAndExecInChild - sys.UidMappings")
	}

	return pid, 0
}

const _LINUX_CAPABILITY_VERSION_3 = 0x20080522

type capHeader struct {
	version uint32
	pid     int32
}

type capData struct {
	effective   uint32
	permitted   uint32
	inheritable uint32
}
type caps struct {
	hdr  capHeader
	data [2]capData
}

// See CAP_TO_INDEX in linux/capability.h:
func capToIndex(cap uintptr) uintptr { return cap >> 5 }

// See CAP_TO_MASK in linux/capability.h:
func capToMask(cap uintptr) uint32 { return 1 << uint(cap&31) }

// cloneArgs holds arguments for clone3 Linux syscall.
type cloneArgs struct {
	flags      uint64 // Flags bit mask
	pidFD      uint64 // Where to store PID file descriptor (int *)
	childTID   uint64 // Where to store child TID, in child's memory (pid_t *)
	parentTID  uint64 // Where to store child TID, in parent's memory (pid_t *)
	exitSignal uint64 // Signal to deliver to parent on child termination
	stack      uint64 // Pointer to lowest byte of stack
	stackSize  uint64 // Size of stack
	tls        uint64 // Location of new TLS
	setTID     uint64 // Pointer to a pid_t array (since Linux 5.5)
	setTIDSize uint64 // Number of elements in set_tid (since Linux 5.5)
	cgroup     uint64 // File descriptor for target cgroup of child (since Linux 5.7)
}

// forkAndExecInChild1 implements the body of forkAndExecInChild up to
// the parent's post-fork path. This is a separate function so we can
// separate the child's and parent's stack frames if we're using
// vfork.
//
// This is go:noinline because the point is to keep the stack frames
// of this and forkAndExecInChild separate.
//
// func forkAndExecInChild1(argv0 *byte, argv, envv []*byte, chroot, dir *byte, attr *ProcAttr, sys *SysProcAttr, pipe int) (pid uintptr, err1 Errno, mapPipe [2]int, locked bool) {

//go:noinline
//go:norace
//go:nocheckptr
func forkAndExecInChild1(argv0 *c.Char, argv, envv **c.Char, chroot, dir *c.Char, attr *ProcAttr, sys *SysProcAttr, pipe int) (pid uintptr, err1 Errno, mapPipe [2]int, locked bool) {
	// Defined in linux/prctl.h starting with Linux 4.3.
	const (
		PR_CAP_AMBIENT       = 0x2f
		PR_CAP_AMBIENT_RAISE = 0x2
	)

	// vfork requires that the child not touch any of the parent's
	// active stack frames. Hence, the child does all post-fork
	// processing in this stack frame and never returns, while the
	// parent returns immediately from this frame and does all
	// post-fork processing in the outer frame.
	//
	// Declare all variables at top in case any
	// declarations require heap allocation (e.g., err2).
	// ":=" should not be used to declare any variable after
	// the call to runtime_BeforeFork.
	//
	// NOTE(bcmills): The allocation behavior described in the above comment
	// seems to lack a corresponding test, and it may be rendered invalid
	// by an otherwise-correct change in the compiler.
	var (
		err2                      Errno
		nextfd                    int
		i                         int
		caps                      caps
		fd1, flags                uintptr
		puid, psetgroups, pgid    []byte
		uidmap, setgroups, gidmap []byte
		clone3                    *cloneArgs
		pgrp                      int32
		dirfd                     int
		cred                      *Credential
		ngroups, groups           uintptr
		// c                         uintptr
	)

	rlim, rlimOK := origRlimitNofile.Load().(Rlimit)

	_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _ = rlim, rlimOK, psetgroups, groups, err2, caps, fd1, uidmap, puid, pgid, gidmap, setgroups, pgrp, dirfd, ngroups, i

	if sys.UidMappings != nil {
		/*
			puid = []byte("/proc/self/uid_map\000")
			uidmap = formatIDMappings(sys.UidMappings)
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.UidMappings")
	}

	if sys.GidMappings != nil {
		/*
			psetgroups = []byte("/proc/self/setgroups\000")
			pgid = []byte("/proc/self/gid_map\000")

			if sys.GidMappingsEnableSetgroups {
				setgroups = []byte("allow\000")
			} else {
				setgroups = []byte("deny\000")
			}
			gidmap = formatIDMappings(sys.GidMappings)
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.GidMappings")
	}

	// Record parent PID so child can test if it has died.
	/**
	ppid, _ := rawSyscallNoError(SYS_GETPID, 0, 0, 0)
	*/

	// Guard against side effects of shuffling fds below.
	// Make sure that nextfd is beyond any currently open files so
	// that we can't run the risk of overwriting any of them.
	fd := make([]int, len(attr.Files))
	nextfd = len(attr.Files)
	for i, ufd := range attr.Files {
		if nextfd < int(ufd) {
			nextfd = int(ufd)
		}
		fd[i] = int(ufd)
	}
	nextfd++

	// Allocate another pipe for parent to child communication for
	// synchronizing writing of User ID/Group ID mappings.
	if sys.UidMappings != nil || sys.GidMappings != nil {
		/*
			if err := forkExecPipe(mapPipe[:]); err != nil {
				err1 = err.(Errno)
				return
			}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.UidMappings")
	}

	flags = sys.Cloneflags
	if sys.Cloneflags&CLONE_NEWUSER == 0 && sys.Unshareflags&CLONE_NEWUSER == 0 {
		// The Go origin implement is:
		// flags |= CLONE_VFORK | CLONE_VM
		// llgo use CLONE_VFORK only.
		// https://www.man7.org/linux/man-pages/man2/clone.2.html
		flags |= CLONE_VFORK
	}
	// Whether to use clone3.
	if sys.UseCgroupFD {
		clone3 = &cloneArgs{
			flags:      uint64(flags) | CLONE_INTO_CGROUP,
			exitSignal: uint64(syscall.SIGCHLD),
			cgroup:     uint64(sys.CgroupFD),
		}
	} else if flags&CLONE_NEWTIME != 0 {
		clone3 = &cloneArgs{
			flags:      uint64(flags),
			exitSignal: uint64(syscall.SIGCHLD),
		}
	}

	// About to call fork.
	// No more allocation or calls of non-assembly functions.
	// runtime_BeforeFork()
	locked = true
	if clone3 != nil {
		/**
		pid, err1 = rawVforkSyscall(_SYS_clone3, uintptr(unsafe.Pointer(clone3)), unsafe.Sizeof(*clone3))
		*/
		panic("todo: syscall.forkAndExecInChild1 - clone3 != nil")
	} else {
		flags |= uintptr(syscall.SIGCHLD)
		if runtime.GOARCH == "s390x" {
			// On Linux/s390, the first two arguments of clone(2) are swapped.
			// pid, err1 = rawVforkSyscall(syscall.SYS_CLONE, 0, flags)
			panic("todo: syscall.forkAndExecInChild1 - GOARCH == s390x")
		} else {
			ret := os.Syscall(syscall.SYS_CLONE, flags, 0)
			if ret >= 0 {
				pid = uintptr(ret)
				err1 = Errno(0)
			} else {
				pid = 0
				err1 = Errno(os.Errno())
			}
		}
	}
	if err1 != 0 || pid != 0 {
		// If we're in the parent, we must return immediately
		// so we're not in the same stack frame as the child.
		// This can at most use the return PC, which the child
		// will not modify, and the results of
		// rawVforkSyscall, which must have been written after
		// the child was replaced.
		return
	}

	// Fork succeeded, now in child.

	// Enable the "keep capabilities" flag to set ambient capabilities later.
	if len(sys.AmbientCaps) > 0 {
		/**
		_, _, err1 = RawSyscall6(syscall.SYS_PRCTL, syscall.PR_SET_KEEPCAPS, 1, 0, 0, 0, 0)
		if err1 != 0 {
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.AmbientCaps")
	}

	// Wait for User ID/Group ID mappings to be written.
	if sys.UidMappings != nil || sys.GidMappings != nil {
		/**
		if _, _, err1 = RawSyscall(syscall.SYS_CLOSE, uintptr(mapPipe[1]), 0, 0); err1 != 0 {
			goto childerror
		}
		pid, _, err1 = RawSyscall(syscall.SYS_READ, uintptr(mapPipe[0]), uintptr(unsafe.Pointer(&err2)), unsafe.Sizeof(err2))
		if err1 != 0 {
			goto childerror
		}
		if pid != unsafe.Sizeof(err2) {
			err1 = syscall.EINVAL
			goto childerror
		}
		if err2 != 0 {
			err1 = err2
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.UidMappings")
	}

	// Session ID
	if sys.Setsid {
		/**
		_, _, err1 = RawSyscall(syscall.SYS_SETSID, 0, 0, 0)
		if err1 != 0 {
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.Setsid")
	}

	// Set process group
	if sys.Setpgid || sys.Foreground {
		// Place child in process group.
		/**
		_, _, err1 = RawSyscall(syscall.SYS_SETPGID, 0, uintptr(sys.Pgid), 0)
		if err1 != 0 {
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.Setpgid")
	}

	if sys.Foreground {
		/**
		pgrp = int32(sys.Pgid)
		if pgrp == 0 {
			pid, _ = rawSyscallNoError(syscall.SYS_GETPID, 0, 0, 0)

			pgrp = int32(pid)
		}

		// Place process group in foreground.
		_, _, err1 = RawSyscall(syscall.SYS_IOCTL, uintptr(sys.Ctty), uintptr(syscall.TIOCSPGRP), uintptr(unsafe.Pointer(&pgrp)))
		if err1 != 0 {
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.Foreground")
	}

	// Restore the signal mask. We do this after TIOCSPGRP to avoid
	// having the kernel send a SIGTTOU signal to the process group.
	// runtime_AfterForkInChild()

	// Unshare
	if sys.Unshareflags != 0 {
		/**
		 _, _, err1 = RawSyscall(syscall.SYS_UNSHARE, sys.Unshareflags, 0, 0)
		 if err1 != 0 {
		 	goto childerror
		 }

		 if sys.Unshareflags&CLONE_NEWUSER != 0 && sys.GidMappings != nil {
		 	dirfd = int(syscall.AT_FDCWD)
		 	if fd1, _, err1 = RawSyscall6(syscall.SYS_OPENAT, uintptr(dirfd), uintptr(unsafe.Pointer(&psetgroups[0])), uintptr(syscall.O_WRO∂∂∂NLY), 0, 0, 0); err1 != 0 {
		 		goto childerror
		 	}
		 	pid, _, err1 = RawSyscall(syscall.SYS_WRITE, uintptr(fd1), uintptr(unsafe.Pointer(&setgroups[0])), uintptr(len(setgroups)))
		 	if err1 != 0 {
		 		goto childerror
		 	}
		 	if _, _, err1 = RawSyscall(syscall.SYS_CLOSE, uintptr(fd1), 0, 0); err1 != 0 {
		 		goto childerror
		 	}

		 	if fd1, _, err1 = RawSyscall6(syscall.SYS_OPENAT, uintptr(dirfd), uintptr(unsafe.Pointer(&pgid[0])), uintptr(syscall.O_WRONLY), 0, 0, 0); err1 != 0 {
		 		goto childerror
			}
			pid, _, err1 = RawSyscall(syscall.SYS_WRITE, uintptr(fd1), uintptr(unsafe.Pointer(&gidmap[0])), uintptr(len(gidmap)))
		 	if err1 != 0 {
				goto childerror
		 	}
		 	if _, _, err1 = RawSyscall(syscall.SYS_CLOSE, uintptr(fd1), 0, 0); err1 != 0 {
		 		goto childerror
		 	}
		}

		if sys.Unshareflags&CLONE_NEWUSER != 0 && sys.UidMappings != nil {
			dirfd = int(syscall._AT_FDCWD)
			if fd1, _, err1 = RawSyscall6(syscall.SYS_OPENAT, uintptr(dirfd), uintptr(unsafe.Pointer(&puid[0])), uintptr(syscall.O_WRONLY), 0, 0, 0); err1 != 0 {
				goto childerror
			}
			pid, _, err1 = RawSyscall(syscall.SYS_WRITE, uintptr(fd1), uintptr(unsafe.Pointer(&uidmap[0])), uintptr(len(uidmap)))
			if err1 != 0 {
				goto childerror
			}
			if _, _, err1 = RawSyscall(syscall.SYS_CLOSE, uintptr(fd1), 0, 0); err1 != 0 {
				goto childerror
			}
		}

		// The unshare system call in Linux doesn't unshare mount points
		// mounted with --shared. Systemd mounts / with --shared. For a
		// long discussion of the pros and cons of this see debian bug 739593.
		// The Go model of unsharing is more like Plan 9, where you ask
		// to unshare and the namespaces are unconditionally unshared.
		// To make this model work we must further mark / as MS_PRIVATE.
		// This is what the standard unshare command does.
		if sys.Unshareflags&CLONE_NEWNS == CLONE_NEWNS {
			_, _, err1 = RawSyscall6(syscall.SYS_MOUNT, uintptr(unsafe.Pointer(&none[0])), uintptr(unsafe.Pointer(&slash[0])), 0, syscall.MS_REC|syscall.MS_PRIVATE, 0, 0)
			if err1 != 0 {
				goto childerror
			}
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.Unshareflags")
	}

	// Chroot
	if chroot != nil {
		/**
		_, _, err1 = RawSyscall(syscall.SYS_CHROOT, uintptr(unsafe.Pointer(chroot)), 0, 0)
		if err1 != 0 {
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - chroot")
	}

	// User and groups
	if cred = sys.Credential; cred != nil {
		/**
		ngroups = uintptr(len(cred.Groups))
		groups = uintptr(0)
		if ngroups > 0 {
			groups = uintptr(unsafe.Pointer(&cred.Groups[0]))
		}
		if !(sys.GidMappings != nil && !sys.GidMappingsEnableSetgroups && ngroups == 0) && !cred.NoSetGroups {
			_, _, err1 = RawSyscall(_SYS_setgroups, ngroups, groups, 0)
			if err1 != 0 {
				goto childerror
			}
		}
		_, _, err1 = RawSyscall(syscall.SYS_SETUID, uintptr(cred.Gid), 0, 0)
		if err1 != 0 {
			goto childerror
		}
		_, _, err1 = RawSyscall(syscall.SYS_SETUID, uintptr(cred.Uid), 0, 0)
		if err1 != 0 {
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - chroot")
	}

	if len(sys.AmbientCaps) != 0 {
		// Ambient capabilities were added in the 4.3 kernel,
		// so it is safe to always use _LINUX_CAPABILITY_VERSION_3.
		/**
		caps.hdr.version = _LINUX_CAPABILITY_VERSION_3

		if _, _, err1 = RawSyscall(syscall.SYS_CAPGET, uintptr(unsafe.Pointer(&caps.hdr)), uintptr(unsafe.Pointer(&caps.data[0])), 0); err1 != 0 {
			goto childerror
		}

		for _, c = range sys.AmbientCaps {
			// Add the c capability to the permitted and inheritable capability mask,
			// otherwise we will not be able to add it to the ambient capability mask.
			caps.data[capToIndex(c)].permitted |= capToMask(c)
			caps.data[capToIndex(c)].inheritable |= capToMask(c)
		}

		if _, _, err1 = RawSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&caps.hdr)), uintptr(unsafe.Pointer(&caps.data[0])), 0); err1 != 0 {
			goto childerror
		}

		for _, c = range sys.AmbientCaps {
			_, _, err1 = RawSyscall6(syscall.SYS_PRCTL, PR_CAP_AMBIENT, uintptr(PR_CAP_AMBIENT_RAISE), c, 0, 0, 0)
			if err1 != 0 {
				goto childerror
			}
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - chroot")
	}

	// Chdir
	if dir != nil {
		/**
		_, _, err1 = RawSyscall(syscall.SYS_CHDIR, uintptr(unsafe.Pointer(dir)), 0, 0)
		if err1 != 0 {
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - dir")
	}

	// Parent death signal
	if sys.Pdeathsig != 0 {
		/**
		_, _, err1 = RawSyscall6(syscall.SYS_PRCTL, syscall.PR_SET_PDEATHSIG, uintptr(sys.Pdeathsig), 0, 0, 0, 0)
		if err1 != 0 {
			goto childerror
		}

		// Signal self if parent is already dead. This might cause a
		// duplicate signal in rare cases, but it won't matter when
		// using SIGKILL.
		pid, _ = rawSyscallNoError(syscall.SYS_GETPPID, 0, 0, 0)
		if pid != ppid {
			pid, _ = rawSyscallNoError(syscall.SYS_GETPID, 0, 0, 0)
			_, _, err1 = RawSyscall(syscall.SYS_KILL, pid, uintptr(sys.Pdeathsig), 0)
			if err1 != 0 {
				goto childerror
			}
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.Pdeathsig")
	}

	// Pass 1: look for fd[i] < i and move those up above len(fd)
	// so that pass 2 won't stomp on an fd it needs later.
	if pipe < nextfd {
		/**
		_, _, err1 = RawSyscall(syscall.SYS_DUP3, uintptr(pipe), uintptr(nextfd), syscall.O_CLOEXEC)
		if err1 != 0 {
			goto childerror
		}
		pipe = nextfd
		nextfd++
		*/
		panic("todo: syscall.forkAndExecInChild1 - pipe < nextfd")
	}

	for i = 0; i < len(fd); i++ {
		if fd[i] >= 0 && fd[i] < i {
			if nextfd == pipe { // don't stomp on pipe
				nextfd++
			}
			if ret := os.Dup3(c.Int(fd[i]), c.Int(nextfd), syscall.O_CLOEXEC); ret < 0 {
				err1 = Errno(os.Errno())
				goto childerror
			}
			fd[i] = nextfd
			nextfd++
		}
	}

	// Pass 2: dup fd[i] down onto i.
	for i = 0; i < len(fd); i++ {
		if fd[i] == -1 {
			os.Close(c.Int(i))
			continue
		}
		if fd[i] == i {
			// dup2(i, i) won't clear close-on-exec flag on Linux,
			// probably not elsewhere either.
			if ret := os.Fcntl(c.Int(fd[i]), syscall.F_SETFD, 0); ret < 0 {
				err1 = Errno(os.Errno())
				goto childerror
			}
			continue
		}
		// The new fd is created NOT close-on-exec,
		// which is exactly what we want.
		if ret := os.Dup3(c.Int(fd[i]), c.Int(i), 0); ret < 0 {
			err1 = Errno(os.Errno())
			goto childerror
		}
	}

	// By convention, we don't close-on-exec the fds we are
	// started with, so if len(fd) < 3, close 0, 1, 2 as needed.
	// Programs that know they inherit fds >= 3 will need
	// to set them close-on-exec.
	for i = len(fd); i < 3; i++ {
		os.Close(c.Int(i))
	}

	// Detach fd 0 from tty
	if sys.Noctty {
		/**
		_, _, err1 = RawSyscall(syscall.SYS_IOCTL, 0, uintptr(syscall.TIOCNOTTY), 0)
		if err1 != 0 {
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.Noctty")
	}

	// Set the controlling TTY to Ctty
	if sys.Setctty {
		/**
		_, _, err1 = RawSyscall(syscall.SYS_IOCTL, uintptr(sys.Ctty), uintptr(syscall.TIOCSCTTY), 1)
		if err1 != 0 {
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.Setctty")
	}

	// Restore original rlimit.
	if rlimOK && rlim.Cur != 0 {
		os.Setrlimit(syscall.RLIMIT_NOFILE, (*syscall.Rlimit)(&rlim))
	}

	// Enable tracing if requested.
	// Do this right before exec so that we don't unnecessarily trace the runtime
	// setting up after the fork. See issue #21428.
	if sys.Ptrace {
		/**
		_, _, err1 = RawSyscall(syscall.SYS_PTRACE, uintptr(syscall.PTRACE_TRACEME), 0, 0)
		if err1 != 0 {
			goto childerror
		}
		*/
		panic("todo: syscall.forkAndExecInChild1 - sys.Ptrace")
	}

	// Time to exec.
	os.Execve(argv0, argv, envv)
	/**
	_, _, err1 = RawSyscall(SYS_EXECVE,
		uintptr(unsafe.Pointer(argv0)),
		uintptr(unsafe.Pointer(&argv[0])),
		uintptr(unsafe.Pointer(&envv[0])))
	*/

childerror:
	// send error code on pipe
	os.Write(c.Int(pipe), unsafe.Pointer(&err1), c.SizeT(unsafe.Sizeof(err1)))
	for {
		os.Exit(253)
	}
}

func formatIDMappings(idMap []SysProcIDMap) []byte {
	/* TODO(xsw):
	var data []byte
	for _, im := range idMap {
		data = append(data, itoa.Itoa(im.ContainerID)+" "+itoa.Itoa(im.HostID)+" "+itoa.Itoa(im.Size)+"\n"...)
	}
	return data
	*/
	panic("todo: syscall.formatIDMappings")
}

// writeIDMappings writes the user namespace User ID or Group ID mappings to the specified path.
func writeIDMappings(path string, idMap []SysProcIDMap) error {
	/* TODO(xsw):
	fd, err := Open(path, O_RDWR, 0)
	if err != nil {
		return err
	}

	if _, err := Write(fd, formatIDMappings(idMap)); err != nil {
		Close(fd)
		return err
	}

	if err := Close(fd); err != nil {
		return err
	}

	return nil
	*/
	panic("todo: syscall.writeIDMappings")
}

// writeSetgroups writes to /proc/PID/setgroups "deny" if enable is false
// and "allow" if enable is true.
// This is needed since kernel 3.19, because you can't write gid_map without
// disabling setgroups() system call.
func writeSetgroups(pid int, enable bool) error {
	/* TODO(xsw):
	sgf := "/proc/" + itoa.Itoa(pid) + "/setgroups"
	fd, err := Open(sgf, O_RDWR, 0)
	if err != nil {
		return err
	}

	var data []byte
	if enable {
		data = []byte("allow")
	} else {
		data = []byte("deny")
	}

	if _, err := Write(fd, data); err != nil {
		Close(fd)
		return err
	}

	return Close(fd)
	*/
	panic("todo: syscall.writeSetgroups")
}

// writeUidGidMappings writes User ID and Group ID mappings for user namespaces
// for a process and it is called from the parent process.
func writeUidGidMappings(pid int, sys *SysProcAttr) error {
	/* TODO(xsw):
	if sys.UidMappings != nil {
		uidf := "/proc/" + itoa.Itoa(pid) + "/uid_map"
		if err := writeIDMappings(uidf, sys.UidMappings); err != nil {
			return err
		}
	}

	if sys.GidMappings != nil {
		// If the kernel is too old to support /proc/PID/setgroups, writeSetGroups will return ENOENT; this is OK.
		if err := writeSetgroups(pid, sys.GidMappingsEnableSetgroups); err != nil && err != ENOENT {
			return err
		}
		gidf := "/proc/" + itoa.Itoa(pid) + "/gid_map"
		if err := writeIDMappings(gidf, sys.GidMappings); err != nil {
			return err
		}
	}

	return nil
	*/
	panic("todo: syscall.writeUidGidMappings")
}
