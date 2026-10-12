// Package safepath checks that output paths stay where they are allowed to be:
// inside an allowed folder, with no junctions or symbolic links on the way. A link
// inside an allowed folder could otherwise redirect exmon's writes, renames and
// deletes somewhere else.
package safepath

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Within reports whether path is root itself or inside it. Both are made absolute
// and cleaned, and compared without regard to case, as Windows does.
func Within(root, path string) bool {
	r, err1 := filepath.Abs(root)
	p, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	r, p = strings.ToLower(filepath.Clean(r)), strings.ToLower(filepath.Clean(p))
	if p == r {
		return true
	}
	return strings.HasPrefix(p, strings.TrimSuffix(r, string(filepath.Separator))+string(filepath.Separator))
}

// CheckNoLinks returns an error if any part of path below root that already
// exists is a junction, symbolic link or other reparse point. Parts that don't
// exist yet are fine: exmon creates them as normal folders and files. path must be
// inside root (see Within).
func CheckNoLinks(root, path string) error {
	r, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	p, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(r, p)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := r
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		// Lstat describes the entry itself, without following it if it is a link.
		st, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // nothing below this exists yet
		}
		if err != nil {
			return err
		}
		// Go reports symbolic links as ModeSymlink, and junctions (and other reparse
		// points) as ModeIrregular.
		if st.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			return fmt.Errorf("%s is a junction or link; exmon won't write through it", cur)
		}
	}
	return nil
}

// LocalDrive reports whether p is an absolute path on a local drive letter:
// C:\... or the long form \\?\C:\... (either slash). Everything else counts as
// possibly remote and is never opened: \\server\share, \\?\UNC\..., \\.\UNC\...,
// \\?\GLOBALROOT\Device\Mup\..., a drive-relative C:x, and a drive letter Windows
// reports as a network drive. Opening such a path would make the PC connect to
// that server.
func LocalDrive(p string) bool {
	l := strings.ReplaceAll(p, `/`, `\`)
	l = strings.TrimPrefix(l, `\\?\`)
	if len(l) < 3 || l[1] != ':' || l[2] != '\\' {
		return false
	}
	c := l[0] | 0x20 // lower case
	if c < 'a' || c > 'z' {
		return false
	}
	return !remoteDrive(l[:3])
}

// remoteDrive asks Windows whether a drive root such as C:\ is a network drive.
func remoteDrive(root string) bool {
	r, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return true
	}
	return windows.GetDriveType(r) == windows.DRIVE_REMOTE
}
