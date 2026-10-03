package winnet

import (
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Processes returns the names of running processes without ".exe", e.g. "PanGPA".
func Processes() ([]string, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, wrap("CreateToolhelp32Snapshot", err)
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var names []string
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := windows.UTF16ToString(e.ExeFile[:])
		names = append(names, strings.TrimSuffix(name, ".exe"))
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return names, wrap("Process32Next", err)
	}
	return names, nil
}
