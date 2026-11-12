// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"gamewe-collector/internal/localrun"
)

const unitKind = "Windows service"

func unitCommands(unit string) (stop, start string) {
	return "sc.exe stop " + unit, "sc.exe start " + unit
}

func killCommand(pid int) string { return fmt.Sprintf("taskkill /PID %d", pid) }

// localCollectors returns the Windows service gamewe-collector while it
// runs. A gamewe run started by hand is not looked for: it holds the
// collector lock of its database, which refuseRunning checks as well.
func localCollectors(context.Context) []localrun.Process {
	if pid, ok := serviceRunning(serviceName); ok && pid != selfPID() {
		return []localrun.Process{{PID: pid, Unit: serviceName}}
	}
	return nil
}

// serviceRunning reports whether the service runs (also starting, stopping
// or paused) and its process id. Only query rights are asked for, so it
// works without administrator rights.
func serviceRunning(name string) (pid int, ok bool) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, false
	}
	defer windows.CloseServiceHandle(m)
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, false
	}
	s, err := windows.OpenService(m, n, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return 0, false // not installed
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS_PROCESS
	var need uint32
	if err := windows.QueryServiceStatusEx(s, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &need); err != nil {
		return 0, false
	}
	return int(st.ProcessId), st.CurrentState != windows.SERVICE_STOPPED
}
