package cli

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/launchd"
	"github.com/mvanhorn/agentcookie/internal/macapp"
)

// plistProgram reads ProgramArguments from plist XML without plutil, so the
// doctor works the same in tests on any OS. It returns the agentcookie.app
// launcher in front of the program, if any, and the program itself.
func plistProgram(data []byte) (launcher, target string) {
	_, after, ok := strings.Cut(string(data), "<key>ProgramArguments</key>")
	if !ok {
		return "", ""
	}
	arr, _, ok := strings.Cut(after, "</array>")
	if !ok {
		return "", ""
	}
	var args []string
	for {
		_, rest, ok := strings.Cut(arr, "<string>")
		if !ok {
			break
		}
		v, tail, ok := strings.Cut(rest, "</string>")
		if !ok {
			break
		}
		args = append(args, html.UnescapeString(v))
		arr = tail
		if len(args) == 2 {
			break
		}
	}
	switch {
	case len(args) == 0:
		return "", ""
	case macapp.IsLauncher(args[0]) && len(args) > 1:
		return args[0], args[1]
	}
	return "", args[0]
}

// loginItemsEnv is what checkLoginItems reads.
type loginItemsEnv struct {
	home         string
	validateApp  func(app string) error
	verifyTarget func(target string) error
}

// checkLoginItems reports whether the agentcookie LaunchAgents show as
// "agentcookie" in System Settings > General > Login Items, and whether a
// job that starts through agentcookie.app can still start. It WARNs at
// worst, never FAILs: scripts/install-beta.sh treats a doctor FAIL as
// "run the wizard", which would ask a sink to pair again.
func checkLoginItems(e loginItemsEnv) Check {
	const name = "Login Items"
	app := macapp.Path(e.home)
	appErr := e.validateApp(app)
	_, markerErr := os.Lstat(launchd.RevertMarker(e.home))
	reverted := markerErr == nil

	var ok, notes, warns []string
	remedy := ""
	warn := func(msg, fix string) {
		warns = append(warns, msg)
		if remedy == "" {
			remedy = fix
		}
	}
	seen := 0
	for _, role := range managedRoles {
		label := launchd.Spec{Role: role}.Label()
		data, err := os.ReadFile(filepath.Join(e.home, "Library", "LaunchAgents", label+".plist"))
		if err != nil {
			continue
		}
		launcher, target := plistProgram(data)
		if target == "" {
			continue
		}
		seen++
		switch {
		case role == launchd.RoleSink && launcher != "":
			warn(label+" starts through agentcookie.app, but the sink must run agentcookie directly to keep its Chrome Automation grant",
				"run `agentcookie services refresh`")
		case role == launchd.RoleSink:
			notes = append(notes, label+" runs agentcookie directly on purpose (its Chrome Automation grant must stay agentcookie's), so it shows the signer's name")
		case launcher != "":
			if _, err := os.Stat(launcher); err != nil {
				warn(fmt.Sprintf("%s starts through %s, which is missing, so it cannot start", label, launcher),
					"run `agentcookie services refresh`; do not delete ~/Applications/agentcookie.app while jobs use it")
			} else if appErr != nil {
				warn(fmt.Sprintf("%s starts through %s, which fails its signature check (%v)", label, app, appErr),
					"run `agentcookie services refresh`")
			} else if err := e.verifyTarget(target); err != nil {
				warn(fmt.Sprintf("the agentcookie.app launcher refuses %s (not a release-signed agentcookie), so %s is stopped", target, label),
					"run `agentcookie services refresh` to run it directly, or install a release build")
			} else {
				ok = append(ok, label)
			}
		case reverted:
			warn(label+" runs agentcookie directly because `services refresh --revert` was run, so Login Items shows the signer's name",
				"run `agentcookie services refresh --wrap` to show it as agentcookie")
		case appErr != nil:
			warn(label+" shows the signer's name in Login Items: agentcookie.app is not installed or fails its signature check",
				"install a release build of agentcookie, then run `agentcookie services refresh`")
		case e.verifyTarget(target) != nil:
			notes = append(notes, fmt.Sprintf("%s runs %s, which is not release-signed (a development build), so Login Items shows the signer's name", label, target))
		default:
			warn(label+" still shows the signer's name in Login Items", "run `agentcookie services refresh`")
		}
	}
	switch {
	case seen == 0:
		return Check{Name: name, Severity: SeveritySkipped, Detail: "no agentcookie LaunchAgents installed"}
	case len(warns) > 0:
		return Check{Name: name, Severity: SeverityWarn, Detail: strings.Join(append(warns, notes...), "; "), Remediation: remedy}
	case len(ok) > 0 && len(notes) == 0:
		return Check{Name: name, Severity: SeverityOK, Detail: strings.Join(ok, ", ") + ` show as "agentcookie" in Login Items`}
	case len(ok) > 0:
		return Check{Name: name, Severity: SeverityOK, Detail: strings.Join(ok, ", ") + ` show as "agentcookie" in Login Items; ` + strings.Join(notes, "; ")}
	}
	return Check{Name: name, Severity: SeverityInfo, Detail: strings.Join(notes, "; ")}
}
