// Package device describes the machine exmon is running on: computer name, user,
// domain, hardware model and Windows version. It replaces the script's WMI query
// with plain Win32 and registry reads.
package device

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Info identifies this machine.
type Info struct {
	Computer string `json:"computer"`
	User     string `json:"user"`
	Domain   string `json:"domain,omitempty"`
	Model    string `json:"model,omitempty"`
	OS       string `json:"os"`
}

// String is the one-line form used in the DEVICE log line and incident headers.
func (i Info) String() string {
	return fmt.Sprintf("computer=%q user=%q domain=%q model=%q os=%q", i.Computer, i.User, i.Domain, i.Model, i.OS)
}

// Get reads the device information. Fields that can't be read are left empty.
func Get() Info {
	host, _ := os.Hostname()
	return Info{
		Computer: host,
		User:     os.Getenv("USERNAME"),
		Domain:   computerName(windows.ComputerNameDnsDomain),
		Model:    model(),
		OS:       osVersion(),
	}
}

// model is the system model, or the motherboard when the BIOS left the system
// model as a placeholder (common on self-built PCs).
func model() string {
	const bios = `HARDWARE\DESCRIPTION\System\BIOS`
	m := regString(bios, "SystemProductName")
	switch strings.ToLower(m) {
	case "", "system product name", "to be filled by o.e.m.", "default string":
		board := strings.TrimSpace(regString(bios, "BaseBoardManufacturer") + " " + regString(bios, "BaseBoardProduct"))
		if board != "" {
			return board
		}
	}
	return m
}

// computerName wraps GetComputerNameEx for one of the COMPUTER_NAME_FORMAT values.
func computerName(format uint32) string {
	buf := make([]uint16, 256)
	n := uint32(len(buf))
	if err := windows.GetComputerNameEx(format, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// osVersion returns e.g. "Windows 11 Enterprise 24H2 (build 26200)".
func osVersion() string {
	v := windows.RtlGetVersion() // the real version, not the one Windows fakes for old apps
	const key = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	name := regString(key, "ProductName")
	// Windows 11 still says "Windows 10" in ProductName; build 22000+ is Windows 11.
	if v.BuildNumber >= 22000 {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1)
	}
	if name == "" {
		name = fmt.Sprintf("Windows %d.%d", v.MajorVersion, v.MinorVersion)
	}
	if dv := regString(key, "DisplayVersion"); dv != "" {
		name += " " + dv
	}
	return fmt.Sprintf("%s (build %d)", name, v.BuildNumber)
}

func regString(path, value string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, err := k.GetStringValue(value)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}
