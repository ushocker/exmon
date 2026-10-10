// Package winsys wraps the Windows security and session APIs exmon needs to run
// safely as a service: setting folder permissions, checking that a file can only be
// changed by administrators, finding the logged-on user, and checking elevation.
//
// Like internal/wlan and internal/winnet, this is one of the few packages allowed to
// use unsafe/uintptr. Everything it returns is a plain Go value.
package winsys

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sidOwnerRights is OWNER RIGHTS. An ACE for it replaces the rights an owner
// normally gets automatically (including changing the permissions), so a read-only
// OWNER RIGHTS ACE makes the owner harmless.
const sidOwnerRights = "S-1-3-4"

// SIDs that are allowed to own or write files exmon trusts.
const (
	sidSystem           = "S-1-5-18"
	sidAdministrators   = "S-1-5-32-544"
	sidTrustedInstaller = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
)

func trusted(sid *windows.SID) bool {
	switch sid.String() {
	case sidSystem, sidAdministrators, sidTrustedInstaller:
		return true
	}
	return false
}

// Access rights that let someone change a file or a folder's contents, or its
// permissions or owner. Rights such as reading or changing attributes don't count.
const writeRights = 0x00000002 | // FILE_WRITE_DATA (folder: add file)
	0x00000004 | // FILE_APPEND_DATA (folder: add subfolder)
	0x00000040 | // FILE_DELETE_CHILD
	0x00010000 | // DELETE
	0x00040000 | // WRITE_DAC
	0x00080000 | // WRITE_OWNER
	0x10000000 | // GENERIC_ALL
	0x40000000 // GENERIC_WRITE

// IsElevated reports whether this process runs with administrator rights.
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// ApplyDACL replaces a file's or folder's permissions with the ones in an SDDL
// string, and stops it inheriting permissions from its parent. If the SDDL names an
// owner ("O:BA"), the owner is set too; that needs administrator rights.
func ApplyDACL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parse permissions %q: %w", sddl, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	owner, _, _ := sd.Owner()
	if owner != nil {
		info |= windows.OWNER_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, owner, nil, dacl, nil); err != nil {
		return fmt.Errorf("set permissions on %s: %w", path, err)
	}
	return nil
}

// InheritDACL replaces path's own permissions with its folder's: it removes every
// explicit entry and turns inheritance on. A file renamed into a folder keeps the
// permissions it had in the old folder (Windows doesn't recompute them on a move),
// so the helper calls this after moving an update into Program Files.
func InheritDACL(path string) error {
	// "D:" is an empty, unprotected DACL. It must be an empty ACL, never a nil
	// one: a nil DACL would mean "no restrictions at all".
	sd, err := windows.SecurityDescriptorFromString("D:")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errors.New("empty DACL came back nil")
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("reset permissions on %s: %w", path, err)
	}
	return nil
}

// AdminOnlyWritable reports whether only SYSTEM, Administrators and TrustedInstaller
// can change path (its contents, permissions or owner). If not, who says which
// account can, for the error message.
func AdminOnlyWritable(path string) (ok bool, who string, err error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, "", fmt.Errorf("read permissions of %s: %w", path, err)
	}
	return checkDescriptor(sd)
}

// checkDescriptor is AdminOnlyWritable on an already-read security descriptor.
func checkDescriptor(sd *windows.SECURITY_DESCRIPTOR) (bool, string, error) {
	owner, _, err := sd.Owner()
	if err != nil {
		return false, "", err
	}

	dacl, _, err := sd.DACL()
	if err == windows.ERROR_OBJECT_NOT_FOUND {
		dacl, err = nil, nil // no DACL at all: same as a NULL DACL
	}
	if err != nil {
		return false, "", err
	}
	if dacl == nil { // a missing or NULL DACL means everyone has full access
		return false, "everyone (no permissions set)", nil
	}
	ownerRightsSet := false // a read-only OWNER RIGHTS entry neutralises the owner
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, "", err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue // a deny entry, or one that only applies to children
		}
		// The SID starts where the SidStart field is and runs on past the struct.
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.String() == sidOwnerRights {
			ownerRightsSet = true
		}
		if uint32(ace.Mask)&writeRights != 0 && !trusted(sid) {
			return false, accountName(sid), nil
		}
	}
	// The owner can normally always change the permissions, so it must be trusted
	// unless an OWNER RIGHTS entry (checked above for write rights) takes that away.
	if !ownerRightsSet && (owner == nil || !trusted(owner)) {
		return false, "owner " + accountName(owner), nil
	}
	return true, "", nil
}

// ServiceSID returns the SID of a service's own identity, "NT SERVICE\<name>".
// The service must already be installed with an unrestricted service SID.
func ServiceSID(service string) (string, error) {
	sid, _, _, err := windows.LookupSID("", `NT SERVICE\`+service)
	if err != nil {
		return "", fmt.Errorf("look up NT SERVICE\\%s: %w", service, err)
	}
	return sid.String(), nil
}

// accountName is "DOMAIN\name" for a SID, or the SID text if it can't be resolved.
func accountName(sid *windows.SID) string {
	if sid == nil {
		return "(none)"
	}
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	if domain != "" {
		return domain + `\` + account
	}
	return account
}
