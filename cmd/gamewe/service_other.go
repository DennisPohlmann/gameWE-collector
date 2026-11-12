// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package main

// serviceMain: only the Windows service manager starts gamewe as a service.
func serviceMain() (code int, ok bool) { return 0, false }

// osCommand runs the commands of one platform (Windows: service); none here.
func osCommand(string, []string) (bool, error) { return false, nil }
