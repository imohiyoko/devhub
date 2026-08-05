//go:build !darwin

package hostspec

// Detect reports nothing on every OS but macOS, and that is not a gap waiting
// to be filled: the only thing devhub sizes against a host budget is a Colima
// VM, and Colima is macOS-only (internal/container refuses elsewhere with
// ErrColimaUnsupportedOS). There is no caller here to serve.
//
// Cores would resolve everywhere — runtime.NumCPU needs no build tag — but
// returning a half-answer would make Detected true on a host where memory is
// unknown, and a memory cap of zero is exactly the misreading Detected exists
// to prevent. Better a clean "devhub cannot tell" that falls back to the
// absolute limits.
func Detect(string) Spec { return Spec{} }

// ColimaDir has nothing to point at where Colima cannot run.
func ColimaDir() string { return "" }
