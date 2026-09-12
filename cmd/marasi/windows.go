//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func secureInstancesDir(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("getting current user: %w", err)
	}

	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"D:P(A;OICI;FA;;;%s)(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)",
		user.User.Sid,
	))
	if err != nil {
		return fmt.Errorf("building access rules: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("reading access rules: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("setting access rules: %w", err)
	}
	return nil
}

func secureInstanceFile(string) error {
	return nil
}
