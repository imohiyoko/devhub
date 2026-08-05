package hostspec

import (
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

// Detect reads this Mac's capacity. dir names the volume whose free space is
// wanted; an empty or unreadable dir leaves FreeDiskBytes at zero rather than
// failing the whole probe, because the disk figure is the one value nothing
// refuses on.
//
// Partial answers are still Detected. Cores always resolve, and memory is the
// number the caps are actually built from — losing the disk figure costs a line
// in the UI, not a decision.
func Detect(dir string) Spec {
	spec := Spec{CPUs: runtime.NumCPU()}

	// hw.memsize is the physical RAM the machine is built with, which is the
	// bound that matters: Lima cannot back a VM with memory that does not
	// exist. Not hw.usermem or the free page count — those move second to
	// second, and a limit that changes while the user is typing into the field
	// is not a limit they can plan against.
	mem, err := unix.SysctlUint64("hw.memsize")
	if err != nil || mem == 0 {
		return Spec{}
	}
	spec.MemoryBytes = int64(mem)

	if dir != "" {
		// x/sys/unix throughout rather than a mix: syscall is soft-deprecated,
		// and one package for both calls means one set of type widths to reason
		// about when this grows a second OS.
		var st unix.Statfs_t
		if err := unix.Statfs(dir, &st); err == nil {
			// Bavail, not Bfree: the blocks a non-root process may actually
			// use. Reporting the root reserve as free space would overstate
			// what the user has by a few percent of the volume.
			spec.FreeDiskBytes = int64(st.Bavail) * int64(st.Bsize)
		}
	}

	spec.Detected = true
	return spec
}

// ColimaDir is the directory whose free space bounds a Colima disk image. It is
// where colima keeps its Lima instances; the home directory stands in when that
// does not exist yet, since on a Mac it is the same volume.
func ColimaDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	colima := filepath.Join(home, ".colima")
	if _, err := os.Stat(colima); err == nil {
		return colima
	}
	return home
}
