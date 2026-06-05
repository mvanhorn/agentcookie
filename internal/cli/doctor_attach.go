package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/mvanhorn/agentcookie/internal/agentattach"
	"github.com/mvanhorn/agentcookie/internal/agentbrowser"
)

// checkAgentBrowserAttach reports whether the Chromium agent browsers are
// attached to a real Chrome over CDP: is the debug endpoint reachable, and
// is each installed agent browser wired to it. This is the doctor side of
// the "are you sure you're logged in?" diagnosis.
func checkAgentBrowserAttach() Check {
	d := agentattach.Discover(context.Background(), agentattach.DefaultDebugPort)
	return checkAgentBrowserAttachWith(d, agentbrowser.All())
}

func checkAgentBrowserAttachWith(d agentattach.Discovery, wirers []agentbrowser.Wirer) Check {
	const name = "agent browser attach"

	var installed, wired, unwired []string
	for _, w := range wirers {
		if !w.IsInstalled() {
			continue
		}
		installed = append(installed, w.Name())
		if agentbrowser.IsWired(w) {
			wired = append(wired, w.Name())
		} else {
			unwired = append(unwired, w.Name())
		}
	}

	if len(installed) == 0 {
		return Check{
			Name:     name,
			Severity: SeveritySkipped,
			Detail:   "no Chromium agent browser installed (browser-use / agent-browser)",
		}
	}

	if !d.Reachable {
		return Check{
			Name:        name,
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("Chrome debug endpoint not reachable (tier %s); installed: %s", d.Tier, strings.Join(installed, ", ")),
			Remediation: d.Remediation,
		}
	}

	if len(wired) == 0 {
		return Check{
			Name:        name,
			Severity:    SeverityWarn,
			Detail:      fmt.Sprintf("Chrome is attachable but no agent browser is wired (installed: %s)", strings.Join(installed, ", ")),
			Remediation: "run `agentcookie attach --wire`",
		}
	}

	detail := fmt.Sprintf("attached: %s wired to Chrome on CDP", strings.Join(wired, ", "))
	if len(unwired) > 0 {
		return Check{
			Name:        name,
			Severity:    SeverityOK,
			Detail:      detail + fmt.Sprintf("; not wired: %s", strings.Join(unwired, ", ")),
			Remediation: "run `agentcookie attach --wire` to wire the rest",
		}
	}
	return Check{Name: name, Severity: SeverityOK, Detail: detail}
}
