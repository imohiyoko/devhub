package envs

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/imohiyoko/devhub/internal/platform"
)

// applescriptEscape escapes a string for an AppleScript double-quoted literal:
// backslash/quote are escaped, CR dropped, LF -> "\n" (literal).
func applescriptEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// shlexSafe matches strings that are safe to leave unquoted in a POSIX shell
// (ASCII \w plus @%+=:,./-).
var shlexSafe = regexp.MustCompile(`^[a-zA-Z0-9_@%+=:,./-]+$`)

// shellQuote applies POSIX shell single-quoting for safe interpolation.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if shlexSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// mergedEnv returns os.Environ() overlaid with extra (extra wins). The PATH
// value is sanitized (sanitizePath) so a newline-corrupted PATH inherited from
// this process does not propagate to spawned children, where it would break
// resolution of executables in PATH subdirectories.
func mergedEnv(extra map[string]string) []string {
	m := map[string]string{}
	for _, e := range os.Environ() {
		if k, v, ok := strings.Cut(e, "="); ok {
			m[k] = v
		}
	}
	maps.Copy(m, extra)
	out := make([]string, 0, len(m))
	for k, v := range m {
		if strings.EqualFold(k, "PATH") {
			v = sanitizePath(v)
		}
		out = append(out, k+"="+v)
	}
	return out
}

// sanitizePath repairs a PATH value that has had CR/LF characters spliced into
// it. This is a real corruption seen on Windows, where a registry Path value
// gains a stray newline; because CreateProcess only auto-searches System32 (not
// its subdirectories), the newline severs resolution of executables that live in
// a PATH subdirectory — most notably powershell.exe under
// System32\WindowsPowerShell\v1.0 — and every exec fails with 0x80070002. Any CR
// or LF is treated as an entry break and empty entries are dropped, so
// "…\Wbem;<LF>C:\…\v1.0" collapses back to "…\Wbem;C:\…\v1.0". A clean PATH is
// returned untouched (no reordering).
func sanitizePath(v string) string {
	if !strings.ContainsAny(v, "\r\n") {
		return v
	}
	sep := os.PathListSeparator
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == '\r' || r == '\n' || r == sep
	})
	return strings.Join(parts, string(sep))
}

// resolveShell resolves a shell name (e.g. "powershell") to an absolute path,
// searching a CR/LF-sanitized copy of PATH. Passing an absolute path is the only
// robust fix for the Windows Terminal launch: wt hands the tab off to an
// already-running WindowsTerminal.exe that resolves the shell against its own
// (possibly stale, possibly newline-corrupted) PATH, ignoring the env we set on
// the wt process — so an absolute path is the one thing that survives. Falls
// back to the bare name when resolution fails, preserving prior behavior.
func resolveShell(shell string) string {
	if shell == "" || strings.ContainsAny(shell, `/\`) {
		return shell // empty, or already an explicit path
	}
	if p, ok := lookPathIn(shell, sanitizePath(os.Getenv("PATH"))); ok {
		return p
	}
	return shell
}

// lookPathIn resolves an executable name against an explicit PATH string, rather
// than the process's live PATH that exec.LookPath is hardwired to read (which
// may be the corrupted value we are trying to route around). On Windows it
// honors PATHEXT so a bare "powershell" matches "powershell.exe"; on Unix it
// requires the executable bit. Non-regular or non-executable candidates are
// skipped so a later PATH entry can still win (matching exec.LookPath).
func lookPathIn(name, pathEnv string) (string, bool) {
	exts := []string{""}
	if platform.IsWindows() && filepath.Ext(name) == "" {
		pathext := os.Getenv("PATHEXT")
		if pathext == "" {
			pathext = ".COM;.EXE;.BAT;.CMD"
		}
		for _, e := range strings.Split(pathext, ";") {
			if e = strings.TrimSpace(e); e != "" {
				exts = append(exts, e)
			}
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		for _, e := range exts {
			cand := filepath.Join(dir, name) + e
			if fi, err := os.Stat(cand); err == nil && fi.Mode().IsRegular() {
				if platform.IsWindows() || fi.Mode().Perm()&0o111 != 0 {
					return cand, true
				}
			}
		}
	}
	return "", false
}

func inPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// runShell launches command via the platform shell (subprocess shell=True).
// The shell invocation is built per-OS in shell_windows.go / shell_unix.go —
// Windows needs a raw command line (SysProcAttr.CmdLine) because cmd.exe does
// not understand Go's default \" argument escaping.
func (c *Controller) runShell(cwd, command string, env []string) error {
	cmd := shellCmd(command)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = env
	return c.spawn(cmd)
}

// termConfig reads the terminal settings for the current OS.
func (c *Controller) termConfig() (emulator, shell string, shellArgs []string) {
	settings, _ := c.store.LoadSettings()
	term, _ := settings["terminal"].(map[string]any)
	cfg, _ := term[platform.SystemName()].(map[string]any)
	emulator, _ = cfg["emulator"].(string)
	shell, _ = cfg["shell"].(string)
	if shell == "" {
		if platform.IsWindows() {
			shell = "powershell"
		} else {
			shell = "bash"
		}
	}
	for _, a := range toAnySlice(cfg["shell_args"]) {
		if s, ok := a.(string); ok {
			shellArgs = append(shellArgs, s)
		}
	}
	return emulator, shell, shellArgs
}

// buildCmdWithEnv prepends per-OS env exports to command (mirrors the
// cmd_with_env construction in open_in_terminal). Env keys are sorted for
// deterministic output.
func buildCmdWithEnv(command string, env map[string]string, isWindows, isPowershell bool) string {
	if len(env) == 0 {
		return command
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var exports []string
	for _, k := range keys {
		v := env[k]
		switch {
		case isWindows && isPowershell:
			exports = append(exports, "$env:"+k+"='"+strings.ReplaceAll(v, "'", "''")+"'")
		case isWindows && !isPowershell:
			exports = append(exports, `set "`+k+"="+strings.ReplaceAll(v, `"`, `\"`)+`"`)
		default:
			exports = append(exports, "export "+k+"="+shellQuote(v))
		}
	}
	sep := " && "
	if isWindows && isPowershell {
		// A newline, not ';': on Windows this PowerShell form is written to a launch
		// script (writeLaunchScript) for the wt path, where a newline is the natural
		// .ps1 statement separator. It must not be ';' — wt corrupts a -Command value
		// containing ';' (or an inline newline), which is exactly why the script is
		// passed via -File instead of -Command (microsoft/terminal#11314).
		sep = "\n"
	} else if isWindows {
		sep = " & "
	}
	return strings.Join(exports, sep) + sep + command
}

// writeLaunchScript writes a composed shell command to a temp .ps1 so the Windows
// Terminal launch can hand PowerShell a file path instead of an inline -Command.
// wt corrupts a -Command value containing ';' or a newline (its parser splits or
// chokes on them — microsoft/terminal#11314); a script path has neither.
// PowerShell parses the whole file up front, so the caller can reclaim it shortly
// after (removeLater).
func writeLaunchScript(command string) (string, error) {
	f, err := os.CreateTemp("", "devhub-launch-*.ps1")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(command + "\r\n"); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// removeLater deletes a launch script after a grace period long enough for
// PowerShell to have parsed it. Best-effort: a temp file leaked by an early crash
// is harmless.
func removeLater(path string) {
	time.Sleep(30 * time.Second)
	_ = os.Remove(path)
}

// openInTerminal launches command in a new terminal window at cwd, injecting
// env. The returned error means the terminal (or shell) could not be started
// at all — it says nothing about whether the user's command then succeeded,
// which devhub cannot observe once the emulator owns the process.
func (c *Controller) openInTerminal(cwd, command string, env map[string]string) error {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	sysName := platform.SystemName()
	emulator, shell, shellArgs := c.termConfig()
	isPowershell := strings.Contains(strings.ToLower(shell), "powershell") || strings.Contains(strings.ToLower(shell), "pwsh")
	// Resolve the shell to an absolute path (family detection above uses the
	// configured name). This is what lets the wt tab find powershell when PATH is
	// newline-corrupted, and is harmless elsewhere (falls back to the bare name).
	shell = resolveShell(shell)

	cmdWithEnv := buildCmdWithEnv(command, env, platform.IsWindows(), isPowershell)
	merged := mergedEnv(env)

	exempt := emulator == "Terminal.app" || emulator == "iTerm" || emulator == "wt"
	if emulator == "" || (!exempt && !inPath(emulator)) {
		return c.runShell(cwd, command, merged)
	}

	switch sysName {
	case "Darwin":
		switch emulator {
		case "ghostty":
			// Inline env as exports (cmdWithEnv), like the other emulators: ghostty
			// runs the command via login(1), which does not forward our cmd.Env to
			// the spawned shell, so process-env injection alone is silently dropped.
			args := append([]string{"--working-directory=" + cwd, "--wait-after-command=true", "-e", shell}, shellArgs...)
			args = append(args, "-c", cmdWithEnv)
			cmd := exec.Command("ghostty", args...) //execaudit:envs-run-terminal
			cmd.Env = merged
			return c.spawn(cmd)
		case "Terminal.app":
			shCmd := "cd " + shellQuote(cwd) + " && " + cmdWithEnv
			script := `tell application "Terminal" to do script "` + applescriptEscape(shCmd) + `"`
			return c.spawn(exec.Command("osascript", "-e", script)) //execaudit:envs-run-terminal
		case "iTerm":
			shCmd := "cd " + shellQuote(cwd) + " && " + cmdWithEnv
			script := "\n            tell application \"iTerm\"\n                create window with default profile\n                tell current session of current window\n                    write text \"" + applescriptEscape(shCmd) + "\"\n                end tell\n            end tell\n            "
			return c.spawn(exec.Command("osascript", "-e", script)) //execaudit:envs-run-terminal
		default:
			return c.runShell(cwd, command, merged)
		}
	case "Windows":
		if emulator == "wt" {
			args := append([]string{"new-tab", "--startingDirectory", cwd, shell}, shellArgs...)
			script := ""
			if isPowershell {
				// wt corrupts a -Command value containing ';' or a newline (its arg
				// parser splits/chokes on them — microsoft/terminal#11314), so hand
				// PowerShell a script file: wt only ever sees the path. Env is baked
				// into the script so the port survives however wt spawns the tab. The
				// temp script is local (no mark-of-the-web), so a RemoteSigned policy
				// runs it as-is; we don't override the machine's ExecutionPolicy.
				if p, err := writeLaunchScript(cmdWithEnv); err == nil {
					script = p
					args = append(args, "-File", script)
				} else {
					args = append(args, "-Command", cmdWithEnv) // best-effort fallback
				}
			} else {
				args = append(args, "/c", cmdWithEnv)
			}
			cmd := exec.Command("wt", args...) //execaudit:envs-run-terminal
			cmd.Env = merged
			err := c.spawn(cmd)
			if script != "" {
				go removeLater(script)
			}
			return err
		}
		return c.runShell(cwd, command, merged)
	case "Linux":
		switch emulator {
		case "gnome-terminal":
			args := append([]string{"--working-directory=" + cwd, "--", shell}, shellArgs...)
			args = append(args, "-c", cmdWithEnv)
			cmd := exec.Command("gnome-terminal", args...) //execaudit:envs-run-terminal
			cmd.Env = merged
			return c.spawn(cmd)
		case "xterm":
			args := append([]string{"-e", shell}, shellArgs...)
			args = append(args, "-c", cmdWithEnv)
			cmd := exec.Command("xterm", args...) //execaudit:envs-run-terminal
			cmd.Dir = cwd
			cmd.Env = merged
			return c.spawn(cmd)
		default:
			return c.runShell(cwd, command, merged)
		}
	default:
		return c.runShell(cwd, command, merged)
	}
}

// openTerminalInDir opens an interactive shell at cwd (no command), falling back
// to the editor when no usable emulator is available. Mirrors open_terminal_in_dir.
func (c *Controller) openTerminalInDir(cwd string) error {
	if cwd == "" || !isDir(cwd) {
		return errMsg("worktree directory does not exist")
	}
	sysName := platform.SystemName()
	emulator, _, _ := c.termConfig()

	// The editor fallback is a success: it is the configured behavior when no
	// usable emulator exists, not a failure to report.
	fallback := func() error { c.workspace.OpenInEditor(cwd); return nil }
	if emulator == "" {
		return fallback()
	}

	switch sysName {
	case "Darwin":
		switch {
		case emulator == "ghostty" && inPath("ghostty"):
			return c.spawn(exec.Command("ghostty", "--working-directory="+cwd)) //execaudit:envs-open-terminal
		case emulator == "Terminal.app":
			shCmd := "cd " + shellQuote(cwd)
			safe := strings.ReplaceAll(strings.ReplaceAll(shCmd, `\`, `\\`), `"`, `\"`)
			return c.spawn(exec.Command("osascript", "-e", `tell application "Terminal" to do script "`+safe+`"`)) //execaudit:envs-open-terminal
		case emulator == "iTerm":
			shCmd := "cd " + shellQuote(cwd)
			safe := strings.ReplaceAll(strings.ReplaceAll(shCmd, `\`, `\\`), `"`, `\"`)
			script := "\n            tell application \"iTerm\"\n                create window with default profile\n                tell current session of current window\n                    write text \"" + safe + "\"\n                end tell\n            end tell\n            "
			return c.spawn(exec.Command("osascript", "-e", script)) //execaudit:envs-open-terminal
		default:
			return fallback()
		}
	case "Windows":
		if emulator == "wt" && inPath("wt") {
			return c.spawn(exec.Command("wt", "new-tab", "--startingDirectory", cwd)) //execaudit:envs-open-terminal
		}
		return fallback()
	case "Linux":
		switch {
		case emulator == "gnome-terminal" && inPath("gnome-terminal"):
			return c.spawn(exec.Command("gnome-terminal", "--working-directory="+cwd)) //execaudit:envs-open-terminal
		case emulator == "xterm" && inPath("xterm"):
			cmd := exec.Command("xterm") //execaudit:envs-open-terminal
			cmd.Dir = cwd
			return c.spawn(cmd)
		default:
			return fallback()
		}
	default:
		return fallback()
	}
}
