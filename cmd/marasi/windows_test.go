//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsInstanceSecurity(t *testing.T) {
	t.Run("should protect the instances directory and inherit its access rules", func(t *testing.T) {
		socketPath, lockPath, err := instanceResourcePaths(t.TempDir(), "work")
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		listener, lock, err := claimInstance(socketPath, lockPath)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		defer releaseInstance(listener, socketPath, lock)

		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		principals := []string{user.User.Sid.String(), "BA", "SY"}
		assertWindowsAccessRules(t, filepath.Dir(socketPath), principals, "OICI", true)
		assertWindowsAccessRules(t, lockPath, principals, "ID", false)
		assertWindowsAccessRules(t, socketPath, principals, "ID", false)
	})
}

func assertWindowsAccessRules(t *testing.T, path string, principals []string, flags string, protected bool) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
	}
	if dacl.AceCount != uint16(len(principals)) {
		t.Fatalf("\nwanted:\n%d access rules\ngot:\n%d", len(principals), dacl.AceCount)
	}
	if protected {
		control, _, err := descriptor.Control()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}
		if control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatalf("\nwanted:\nprotected DACL\ngot:\n%s", descriptor)
		}
	}

	sddl := descriptor.String()
	for _, principal := range principals {
		want := flags + ";FA;;;" + principal + ")"
		if !strings.Contains(sddl, want) {
			t.Fatalf("\nwanted:\n%s containing %s\ngot:\n%s", path, want, sddl)
		}
	}
}
