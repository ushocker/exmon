package vpn

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// serverFor finds a built-in VPN entry's server address in a RAS phonebook
// (rasphone.pbk, an INI file: [Entry Name] sections with PhoneNumber=server).
// It looks in the phonebook RAS reported first, then the usual per-user and
// all-users locations.
func serverFor(entry, phonebook string) string {
	paths := []string{phonebook}
	for _, base := range []string{os.Getenv("APPDATA"), os.Getenv("ProgramData")} {
		if base != "" {
			paths = append(paths, filepath.Join(base, `Microsoft\Network\Connections\Pbk\rasphone.pbk`))
		}
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		server := phoneNumber(bufio.NewScanner(f), entry)
		f.Close()
		if server != "" {
			return server
		}
	}
	return ""
}

// phoneNumber reads an INI-format phonebook and returns PhoneNumber= from the
// [entry] section.
func phoneNumber(sc *bufio.Scanner, entry string) string {
	in := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			in = strings.EqualFold(line[1:len(line)-1], entry)
			continue
		}
		if in {
			if v, ok := strings.CutPrefix(line, "PhoneNumber="); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}
