package envs

import (
	"maps"
	"os"
	"os/exec"
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

// mergedEnv returns os.Environ() overlaid with extra (extra wins).
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
		out = append(out, k+"="+v)
	}
	return out
}

func inPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func start(cmd *exec.Cmd) { _ = cmd.Start() }

// runShell launches command via the platform shell (subprocess shell=True).
// The shell invocation is built per-OS in shell_windows.go / shell_unix.go —
// Windows needs a raw command line (SysProcAttr.CmdLine) because cmd.exe does
// not understand Go's default \" argument escaping.
func runShell(cwd, command string, env []string) {
	cmd := shellCmd(command)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = env
	start(cmd)
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

// openInTerminal launches command in a new terminal window at cwd, injecting env.
// Mirrors open_in_terminal.
func (c *Controller) openInTerminal(cwd, command string, env map[string]string) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	sysName := platform.SystemName()
	emulator, shell, shellArgs := c.termConfig()
	isPowershell := strings.Contains(strings.ToLower(shell), "powershell") || strings.Contains(strings.ToLower(shell), "pwsh")

	cmdWithEnv := buildCmdWithEnv(command, env, platform.IsWindows(), isPowershell)
	merged := mergedEnv(env)

	exempt := emulator == "Terminal.app" || emulator == "iTerm" || emulator == "wt"
	if emulator == "" || (!exempt && !inPath(emulator)) {
		runShell(cwd, command, merged)
		return
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
			start(cmd)
		case "Terminal.app":
			shCmd := "cd " + shellQuote(cwd) + " && " + cmdWithEnv
			script := `tell application "Terminal" to do script "` + applescriptEscape(shCmd) + `"`
			start(exec.Command("osascript", "-e", script)) //execaudit:envs-run-terminal
		case "iTerm":
			shCmd := "cd " + shellQuote(cwd) + " && " + cmdWithEnv
			script := "\n            tell application \"iTerm\"\n                create window with default profile\n                tell current session of current window\n                    write text \"" + applescriptEscape(shCmd) + "\"\n                end tell\n            end tell\n            "
			start(exec.Command("osascript", "-e", script)) //execaudit:envs-run-terminal
		default:
			runShell(cwd, command, merged)
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
			start(cmd)
			if script != "" {
				go removeLater(script)
			}
		} else {
			runShell(cwd, command, merged)
		}
	case "Linux":
		switch emulator {
		case "gnome-terminal":
			args := append([]string{"--working-directory=" + cwd, "--", shell}, shellArgs...)
			args = append(args, "-c", cmdWithEnv)
			cmd := exec.Command("gnome-terminal", args...) //execaudit:envs-run-terminal
			cmd.Env = merged
			start(cmd)
		case "xterm":
			args := append([]string{"-e", shell}, shellArgs...)
			args = append(args, "-c", cmdWithEnv)
			cmd := exec.Command("xterm", args...) //execaudit:envs-run-terminal
			cmd.Dir = cwd
			cmd.Env = merged
			start(cmd)
		default:
			runShell(cwd, command, merged)
		}
	default:
		runShell(cwd, command, merged)
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

	fallback := func() { c.workspace.OpenInEditor(cwd) }
	if emulator == "" {
		fallback()
		return nil
	}

	switch sysName {
	case "Darwin":
		switch {
		case emulator == "ghostty" && inPath("ghostty"):
			start(exec.Command("ghostty", "--working-directory="+cwd)) //execaudit:envs-open-terminal
		case emulator == "Terminal.app":
			shCmd := "cd " + shellQuote(cwd)
			safe := strings.ReplaceAll(strings.ReplaceAll(shCmd, `\`, `\\`), `"`, `\"`)
			start(exec.Command("osascript", "-e", `tell application "Terminal" to do script "`+safe+`"`)) //execaudit:envs-open-terminal
		case emulator == "iTerm":
			shCmd := "cd " + shellQuote(cwd)
			safe := strings.ReplaceAll(strings.ReplaceAll(shCmd, `\`, `\\`), `"`, `\"`)
			script := "\n            tell application \"iTerm\"\n                create window with default profile\n                tell current session of current window\n                    write text \"" + safe + "\"\n                end tell\n            end tell\n            "
			start(exec.Command("osascript", "-e", script)) //execaudit:envs-open-terminal
		default:
			fallback()
		}
	case "Windows":
		if emulator == "wt" && inPath("wt") {
			start(exec.Command("wt", "new-tab", "--startingDirectory", cwd)) //execaudit:envs-open-terminal
		} else {
			fallback()
		}
	case "Linux":
		switch {
		case emulator == "gnome-terminal" && inPath("gnome-terminal"):
			start(exec.Command("gnome-terminal", "--working-directory="+cwd)) //execaudit:envs-open-terminal
		case emulator == "xterm" && inPath("xterm"):
			cmd := exec.Command("xterm") //execaudit:envs-open-terminal
			cmd.Dir = cwd
			start(cmd)
		default:
			fallback()
		}
	default:
		fallback()
	}
	return nil
}
