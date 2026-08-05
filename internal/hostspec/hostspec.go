// Package hostspec reports what this machine physically has: cores, memory,
// and free space on a given volume.
//
// It exists so devhub can refuse to ask for something the host cannot give.
// Before it, internal/container carried absolute size limits with a comment
// saying outright that they were not the host's limits because "devhub does not
// know what the host has" — so a 64 GiB request on a 32 GiB Mac was passed
// through and failed at colima, which for a resize means failing *after* the
// stop has already run.
//
// Nothing here spawns a process. Every value comes from a syscall or the Go
// runtime, which is what keeps this out of the execaudit ledger entirely — a
// capacity check that itself shelled out to `sysctl` would be a new external
// command on a path whose whole job is to be the thing that runs before
// anything else does.
//
// Detected is the honest answer when devhub cannot tell. It is not a zero: a
// caller must not read "0 cores" as a limit and refuse everything, which is why
// the flag is separate from the numbers rather than inferred from them.
package hostspec

// Spec is what the host has. Sizes are bytes; CPUs is logical cores.
//
// FreeDiskBytes is deliberately not a limit anywhere in devhub. Lima's disk
// images are sparse, so a profile may legitimately declare more disk than the
// volume has free — this machine runs two profiles declaring 300 GiB on a
// volume with 129 GiB available, and both work. It is reported so a caller can
// show it, not so a caller can refuse with it.
type Spec struct {
	CPUs          int
	MemoryBytes   int64
	FreeDiskBytes int64
	// Detected is false when this OS has no implementation, or when the
	// syscalls failed. Callers fall back to whatever they did before rather
	// than treating the zero values as a host with nothing on it.
	Detected bool
}
