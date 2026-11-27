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

// procTree is a credential plugin and everything it starts, as one thing to kill. On
// Windows that is a Job Object: the plugin is created suspended, put in the job, and only
// then let run, so nothing it starts can leave before the job holds it; terminating the
// job, or closing its last handle (KILL_ON_JOB_CLOSE), ends every process in it. The
// plugin has no console window and its own process group, so it cannot prompt and a
// Ctrl+C at dcctl's console is not delivered to it.
type procTree struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	job  windows.Handle
	shut bool
}

func newProcTree(cmd *exec.Cmd) *procTree {
	t := &procTree{cmd: cmd}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
	cmd.Cancel = func() error { t.kill(); return nil }
	return t
}

var errNotInJob = errors.New("the plugin could not be put in a job object")

func (t *procTree) start() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return errNoProcessTree
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return errNoProcessTree
	}
	t.mu.Lock()
	t.job = job
	t.mu.Unlock()
	if err := t.cmd.Start(); err != nil {
		t.close()
		return err
	}
	pid := uint32(t.cmd.Process.Pid)
	if err := assignAndResume(job, pid); err != nil {
		// Never let a plugin run outside the job: it was created suspended, so it has
		// started nothing yet.
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait()
		t.close()
		return err
	}
	return nil
}

// assignAndResume puts the suspended process pid in job, then resumes its threads.
func assignAndResume(job windows.Handle, pid uint32) error {
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return errNotInJob
	}
	err = windows.AssignProcessToJobObject(job, p)
	_ = windows.CloseHandle(p)
	if err != nil {
		return errNotInJob
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return errNotInJob
	}
	defer windows.CloseHandle(snap)
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			continue
		}
		if _, err := windows.ResumeThread(th); err == nil {
			resumed++
		}
		_ = windows.CloseHandle(th)
	}
	if resumed == 0 {
		return errNotInJob
	}
	return nil
}

// kill terminates every process in the job.
func (t *procTree) kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.shut && t.job != 0 {
		_ = windows.TerminateJobObject(t.job, 1)
	}
}

// close terminates what is left and lets go of the job.
func (t *procTree) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.shut && t.job != 0 {
		_ = windows.TerminateJobObject(t.job, 1)
		_ = windows.CloseHandle(t.job)
	}
	t.shut = true
}
