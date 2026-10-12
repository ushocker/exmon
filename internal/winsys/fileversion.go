package winsys

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FileVersion is the version resource of an exe or DLL (what Explorer shows on
// the Details tab).
type FileVersion struct {
	FileVersion    string // numeric, from the fixed part: "10.0.26100.1"
	ProductVersion string // the ProductVersion string, e.g. "4.18.24090.11"
	ProductName    string
	Company        string
	Description    string // FileDescription
}

// ErrNoVersionInfo means the file has no version resource (common for
// home-grown or script-based services).
var ErrNoVersionInfo = errors.New("no version information")

// ReadFileVersion reads a file's version resource.
func ReadFileVersion(path string) (FileVersion, error) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil {
		if errors.Is(err, windows.ERROR_RESOURCE_TYPE_NOT_FOUND) || errors.Is(err, windows.ERROR_RESOURCE_DATA_NOT_FOUND) {
			return FileVersion{}, ErrNoVersionInfo
		}
		return FileVersion{}, err
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return FileVersion{}, err
	}
	var v FileVersion
	if fixed, ok := fixedInfo(buf); ok {
		v.FileVersion = fmt.Sprintf("%d.%d.%d.%d",
			fixed.FileVersionMS>>16, fixed.FileVersionMS&0xffff, fixed.FileVersionLS>>16, fixed.FileVersionLS&0xffff)
	}
	lang := translation(buf)
	v.ProductVersion = stringValue(buf, lang, "ProductVersion")
	v.ProductName = stringValue(buf, lang, "ProductName")
	v.Company = stringValue(buf, lang, "CompanyName")
	v.Description = stringValue(buf, lang, "FileDescription")
	if v.FileVersion == "" && v.ProductVersion == "" {
		return FileVersion{}, ErrNoVersionInfo
	}
	return v, nil
}

// fixedInfo reads the VS_FIXEDFILEINFO root block ("\").
func fixedInfo(buf []byte) (windows.VS_FIXEDFILEINFO, bool) {
	var p unsafe.Pointer
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&p), &n); err != nil || n < uint32(unsafe.Sizeof(windows.VS_FIXEDFILEINFO{})) {
		return windows.VS_FIXEDFILEINFO{}, false
	}
	return *(*windows.VS_FIXEDFILEINFO)(p), true
}

// translation returns the first language+codepage pair as "040904b0", the key of
// the string table; US English Unicode if the file doesn't list one.
func translation(buf []byte) string {
	var p unsafe.Pointer
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\VarFileInfo\Translation`, unsafe.Pointer(&p), &n); err != nil || n < 4 {
		return "040904b0"
	}
	pair := (*[2]uint16)(p)
	return fmt.Sprintf("%04x%04x", pair[0], pair[1])
}

// stringValue reads one entry of the string table, or "".
func stringValue(buf []byte, lang, name string) string {
	var p unsafe.Pointer
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\StringFileInfo\`+lang+`\`+name, unsafe.Pointer(&p), &n); err != nil || n == 0 {
		return ""
	}
	return windows.UTF16PtrToString((*uint16)(p))
}
