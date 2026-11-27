// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// startTree starts cmd inside a job object of its own, created with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, and makes the end of its context
// (exec.Cmd.Cancel) terminate the whole job: the command and every process it
// spawned, such as an exec credential plugin kubectl started. A process a job member
// creates is in the job too, so nothing the command spawns is outside it.
//
// 🔴 THE COMMAND IS IN THE JOB BEFORE IT RUNS ANY CODE. It is created suspended
// (CREATE_SUSPENDED), assigned to the job, and only then resumed, so it cannot have
// spawned anything that escaped the assignment. PROC_THREAD_ATTRIBUTE_JOB_LIST would
// assign it at creation, but only through a CreateProcess call of dcctl's own, which
// would leave os/exec (and CommandContext's Cancel and WaitDelay) behind; os/exec
// closes the new thread's handle, so the resume finds the thread with a thread
// snapshot instead.
//
// The returned end closes the job, which, being KILL_ON_JOB_CLOSE, also ends anything
// the command left behind when it finished on its own; and if dcctl itself is ended,
// however abruptly, the system closes the handle and ends the tree with it.
//
// A job that cannot be made or assigned (an environment whose own job forbids it)
// leaves the command running as before this existed: killed alone on Cancel. Its answer
// is still the right one; only its descendants are not ended with it.
func startTree(cmd *exec.Cmd) (end func(), err error) {
	t := &jobTree{}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	cmd.Cancel = func() error {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.cancelled = true
		if t.job != 0 {
			return windows.TerminateJobObject(t.job, 1)
		}
		return cmd.Process.Kill()
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := uint32(cmd.Process.Pid)
	t.mu.Lock()
	if !t.cancelled { // a cancelled command was killed while still suspended
		t.job = confine(pid)
	}
	t.mu.Unlock()
	if err := resumeProcess(pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.close()
		return nil, err
	}
	return t.close, nil
}

// jobTree is one command's job. mu orders the assignment against Cancel, which
// exec.Cmd calls from its own goroutine.
type jobTree struct {
	mu        sync.Mutex
	job       windows.Handle
	cancelled bool
}

func (t *jobTree) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

// confine puts the process in a new kill-on-close job and returns the job, or 0 when
// it could not.
func confine(pid uint32) windows.Handle {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0
	}
	// The pid is the command's own: exec.Cmd holds its handle until Wait, so it cannot
	// have been reused.
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		_ = windows.CloseHandle(job)
		return 0
	}
	defer func() { _ = windows.CloseHandle(p) }()
	if err := windows.AssignProcessToJobObject(job, p); err != nil {
		_ = windows.CloseHandle(job)
		return 0
	}
	return job
}

// resumeProcess resumes the threads of a process created suspended: its one initial
// thread, found through a thread snapshot.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if err != nil {
			return err
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	if resumed == 0 {
		return errors.New("the command's thread was not found to resume")
	}
	return nil
}
