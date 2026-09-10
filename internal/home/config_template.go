package home

import (
	"fmt"
	"strings"
)

// DefaultConfigTemplate returns the default configuration as a human-editable
// YAML template with comments and examples.
func DefaultConfigTemplate() []byte {
	cfg := DefaultConfig()

	var out strings.Builder
	out.WriteString("# prutil configuration\n")
	out.WriteString("#\n")
	out.WriteString("# All keys are optional. Remove any key to use prutil's built-in default.\n\n")

	out.WriteString("herdr:\n")
	out.WriteString("  # Optional: restrict handoffs to one agent kind (for example \"claude\" or \"copilot\").\n")
	out.WriteString("  agent_kind: \"\"\n")
	out.WriteString("  # Optional: a herdr skill name that receives each pull request URL.\n")
	out.WriteString("  skill: \"\"\n")
	out.WriteString("  # Prompt template rendered with Repo, Number, URL, Title, HeadRef, BaseRef,\n")
	out.WriteString("  # UnresolvedCount, NewCount and Note.\n")
	out.WriteString("  prompt: |-\n")
	for _, line := range strings.Split(DefaultPrompt, "\n") {
		out.WriteString("    " + line + "\n")
	}
	_, _ = fmt.Fprintf(&out, "  wait_for_idle: %s\n", cfg.Herdr.WaitForIdle)
	_, _ = fmt.Fprintf(&out, "  dry_run: %t\n", cfg.Herdr.DryRun)
	_, _ = fmt.Fprintf(&out, "  toast: %t\n\n", cfg.Herdr.Toast)

	out.WriteString("watch:\n")
	_, _ = fmt.Fprintf(&out, "  active_interval: %s\n", cfg.Watch.ActiveInterval)
	_, _ = fmt.Fprintf(&out, "  base_interval: %s\n", cfg.Watch.BaseInterval)
	_, _ = fmt.Fprintf(&out, "  max_interval: %s\n", cfg.Watch.MaxInterval)
	_, _ = fmt.Fprintf(&out, "  notified_interval: %s\n", cfg.Watch.NotifiedInterval)
	_, _ = fmt.Fprintf(&out, "  max_notified_interval: %s\n", cfg.Watch.MaxNotifiedInterval)
	_, _ = fmt.Fprintf(&out, "  idle_interval: %s\n", cfg.Watch.IdleInterval)
	_, _ = fmt.Fprintf(&out, "  dormant_after: %d\n", cfg.Watch.DormantAfter)
	_, _ = fmt.Fprintf(&out, "  force_precise_every: %d\n\n", cfg.Watch.ForcePreciseEvery)

	out.WriteString("# Optional explicit checkout locations by owner/name.\n")
	out.WriteString("# Example:\n")
	out.WriteString("# repos:\n")
	out.WriteString("#   acme/widgets: ~/src/widgets\n")
	out.WriteString("#   your-org/another-repo: /workspace/another-repo\n")
	out.WriteString("repos: {}\n\n")

	out.WriteString("discovery:\n")
	out.WriteString("  # Optional roots to scan for git checkouts when repos has no entry.\n")
	out.WriteString("  # roots:\n")
	out.WriteString("  #   - ~/src\n")
	out.WriteString("  #   - /workspace\n")
	out.WriteString("  roots: []\n")

	return []byte(out.String())
}
