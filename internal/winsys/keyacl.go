package winsys

// Who may use a CNG private key. A machine key made by Intune (or `exmon enroll`)
// can be used only by SYSTEM and Administrators; the exmon service runs as
// LocalService (NT SERVICE\exmon), so exmon-helper grants that identity read
// access, which for a key means "may sign with it".

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// setPropertyFlags is setProperty with flags (here: which part of a security
// descriptor is being set).
func setPropertyFlags(obj uintptr, name string, value []byte, flags uintptr) error {
	r, _, _ := procNCryptSetProperty.Call(obj, uintptr(unsafe.Pointer(utf16(name))),
		uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)), flags)
	return cngError("NCryptSetProperty("+name+")", r)
}

// getProperty reads a property (size first, then the value).
func getProperty(obj uintptr, name string, flags uintptr) ([]byte, error) {
	var size uint32
	r, _, _ := procNCryptGetProperty.Call(obj, uintptr(unsafe.Pointer(utf16(name))), 0, 0, uintptr(unsafe.Pointer(&size)), flags)
	if err := cngError("NCryptGetProperty("+name+")", r); err != nil {
		return nil, err
	}
	// A []uint64 keeps the buffer 8-byte aligned, as a security descriptor needs.
	words := make([]uint64, size/8+1)
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), len(words)*8)
	r, _, _ = procNCryptGetProperty.Call(obj, uintptr(unsafe.Pointer(utf16(name))), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(size), uintptr(unsafe.Pointer(&size)), flags)
	if err := cngError("NCryptGetProperty("+name+")", r); err != nil {
		return nil, err
	}
	return buf[:size], nil
}

// KeyDACL returns who may use the key, as SDDL (e.g. "D:(A;;GA;;;SY)(A;;GA;;;BA)").
func (s *Signer) KeyDACL() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := getProperty(s.key, propSecurity, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	// The property is a self-relative SECURITY_DESCRIPTOR: one block of memory
	// that can be viewed directly as Windows' structure.
	sd := (*windows.SECURITY_DESCRIPTOR)(unsafe.Pointer(&b[0]))
	return sd.String(), nil
}

// HasKeyAccess reports whether sid already has an allow entry on the key.
func (s *Signer) HasKeyAccess(sid string) (bool, error) {
	d, err := s.KeyDACL()
	if err != nil {
		return false, err
	}
	return strings.Contains(strings.ToUpper(d), ";;;"+strings.ToUpper(sid)+")"), nil
}

// GrantKeyRead lets sid use the key (GENERIC_READ). It does nothing if sid already
// has an entry, and reports whether it changed anything. Changing a key's
// permissions needs an administrator or SYSTEM.
func (s *Signer) GrantKeyRead(sid string) (bool, error) {
	has, err := s.HasKeyAccess(sid)
	if err != nil || has {
		return false, err
	}
	d, err := s.KeyDACL()
	if err != nil {
		return false, err
	}
	sddl := addAllowACE(d, "(A;;GR;;;"+sid+")")
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return false, fmt.Errorf("build the key's permissions %q: %w", sddl, err)
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(sd)), sd.Length())
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := setPropertyFlags(s.key, propSecurity, raw, windows.DACL_SECURITY_INFORMATION); err != nil {
		return false, err
	}
	return true, nil
}

// addAllowACE appends an ACE to the DACL part of an SDDL string. The DACL is the
// "D:" section; anything after it (another section starting "S:") is kept.
func addAllowACE(sddl, ace string) string {
	i := strings.Index(sddl, "D:")
	if i < 0 {
		return sddl + "D:" + ace
	}
	end := len(sddl)
	if j := strings.Index(sddl[i:], "S:"); j >= 0 {
		end = i + j
	}
	return sddl[:end] + ace + sddl[end:]
}
