// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proctree

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Run starts cmd inside a job object of its own, created with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, and waits for it. If ctx ends first, the whole
// job (the command and every process it spawned) is terminated and Run returns ctx's
// error. cmd must not have been made with exec.CommandContext: Run is what binds it to
// ctx. When Run returns the job is closed, which also ends anything the command left
// in it.
//
// 🔴 THE COMMAND IS IN THE JOB BEFORE IT RUNS ANY CODE. It is created suspended
// (CREATE_SUSPENDED), assigned to the job with nothing in between, and only then
// resumed, so it cannot have spawned anything outside the job. A command that cannot
// be put in a job is terminated while still suspended (it has run nothing) and Run
// returns the error: it never runs unconfined. PROC_THREAD_ATTRIBUTE_JOB_LIST would
// assign the job at creation, but only through a CreateProcess call of Run's own,
// which would leave os/exec behind. The window that remains: dcctl itself ended
// between the create and the assignment leaves the command suspended, outside any job.
func Run(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := uint32(cmd.Process.Pid)
	job, err := confine(pid)
	if err != nil {
		_ = cmd.Process.Kill() // still suspended: nothing has run
		_ = cmd.Wait()
		return err
	}
	defer func() { _ = windows.CloseHandle(job) }()
	// Resumed before ctx is watched, so no termination of ours can interleave with the
	// resume; resumeProcess still checks each thread's owner (resumeThread).
	if err := resumeProcess(pid); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_ = cmd.Wait()
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// The job is held by handle: it is this command's tree whenever this runs.
		_ = windows.TerminateJobObject(job, 1)
		<-done
		return ctx.Err()
	}
}

// assignToJob is windows.AssignProcessToJobObject; a variable only so a test can make
// the assignment fail.
var assignToJob = windows.AssignProcessToJobObject

// confine puts the process in a new kill-on-close job.
func confine(pid uint32) (windows.Handle, error) {
	// The pid is the command's own: exec.Cmd holds its handle until Wait, so it cannot
	// have been reused.
	p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	defer func() { _ = windows.CloseHandle(p) }()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	if err := assignToJob(job, p); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// resumeProcess resumes the threads of a process created suspended: its one initial
// thread, found through a thread snapshot, since os/exec closes the handle
// CreateProcess returned for it.
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
		if err := resumeThread(pid, te.ThreadID); err != nil {
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

var procGetProcessIdOfThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIdOfThread")

// resumeThread resumes thread tid, which the snapshot said belongs to pid.
//
// 🔴 THE THREAD IS CHECKED AFTER IT IS OPENED. A thread id in a snapshot is a number,
// not a handle: if the process had ended since (terminated by someone else while
// suspended), the id could name another process's thread by the time it is opened,
// and resuming that would wake a thread its own process had suspended. The open handle
// pins the thread, so its owner is read from the handle, and anything not pid's is
// refused untouched.
func resumeThread(pid, tid uint32) error {
	th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME|windows.THREAD_QUERY_LIMITED_INFORMATION, false, tid)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(th) }()
	owner, _, callErr := procGetProcessIdOfThread.Call(uintptr(th))
	if owner == 0 {
		return fmt.Errorf("reading thread %d's process: %w", tid, callErr)
	}
	if uint32(owner) != pid {
		return fmt.Errorf("thread %d belongs to process %d, not the command's %d", tid, owner, pid)
	}
	_, err = windows.ResumeThread(th)
	return err
}
