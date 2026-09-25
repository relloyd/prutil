package home

import (
	"fmt"
	"strings"
)

// writeSequence writes one indented YAML sequence, as a flow list when it is
// empty so the key still reads as a list rather than as null.
func writeSequence(out *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		_, _ = fmt.Fprintf(out, "  %s: []\n", key)
		return
	}
	_, _ = fmt.Fprintf(out, "  %s:\n", key)
	for _, value := range values {
		_, _ = fmt.Fprintf(out, "    - %q\n", value)
	}
}

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
	_, _ = fmt.Fprintf(&out, "  toast: %t\n", cfg.Herdr.Toast)
	out.WriteString("  # Action when no matching agent is found: \"new\" (provision), \"none\" (stop), or \"repo\" (any agent in repo).\n")
	_, _ = fmt.Fprintf(&out, "  fallback: %s\n\n", cfg.Herdr.Fallback)
	out.WriteString("  # Separate prompt template for failed-check investigations. It receives Repo, Number, URL,\n")
	out.WriteString("  # Title, HeadRef, BaseRef, Checks and Note.\n")
	out.WriteString("  check_prompt: |-\n")
	for _, line := range strings.Split(DefaultCheckPrompt, "\n") {
		out.WriteString("    " + line + "\n")
	}

	out.WriteString("watch:\n")
	_, _ = fmt.Fprintf(&out, "  active_interval: %s\n", cfg.Watch.ActiveInterval)
	_, _ = fmt.Fprintf(&out, "  base_interval: %s\n", cfg.Watch.BaseInterval)
	_, _ = fmt.Fprintf(&out, "  max_interval: %s\n", cfg.Watch.MaxInterval)
	_, _ = fmt.Fprintf(&out, "  notified_interval: %s\n", cfg.Watch.NotifiedInterval)
	_, _ = fmt.Fprintf(&out, "  max_notified_interval: %s\n", cfg.Watch.MaxNotifiedInterval)
	_, _ = fmt.Fprintf(&out, "  idle_interval: %s\n", cfg.Watch.IdleInterval)
	_, _ = fmt.Fprintf(&out, "  dormant_after: %d\n", cfg.Watch.DormantAfter)
	out.WriteString("  # When true, all unresolved review comments from your account are treated\n")
	out.WriteString("  # as actionable feedback unless they carry an agent tracking comment.\n")
	_, _ = fmt.Fprintf(&out, "  self_review: %t\n", cfg.Watch.SelfReview)
	out.WriteString("  # Write this string in one of your own review comments to have the watcher\n")
	out.WriteString("  # treat it as feedback, which is how to try the feature without waiting for\n")
	out.WriteString("  # a reviewer. It answers only for comments you wrote. Set it to \"\" to turn\n")
	out.WriteString("  # it off.\n")
	_, _ = fmt.Fprintf(&out, "  self_test_marker: %q\n", cfg.Watch.Marker())
	_, _ = fmt.Fprintf(&out, "  force_precise_every: %d\n\n", cfg.Watch.ForcePreciseEvery)

	out.WriteString("review:\n")
	out.WriteString("  # Optional: comment posted to a pull request to trigger an AI review.\n")
	out.WriteString("  # Set to \"\" to disable. Defaults to \"/gemini review\".\n")
	_, _ = fmt.Fprintf(&out, "  comment: %q\n", cfg.Review.CommentFor(""))
	out.WriteString("  # Optional per-repository overrides:\n")
	out.WriteString("  # repos:\n")
	out.WriteString("  #   owner/repo: \"@coderabbitai review\"\n\n")

	out.WriteString("notifications:\n")
	out.WriteString("  # Desktop notifications prutil raises when one of your open pull requests\n")
	out.WriteString("  # changes. Press s in prutil to turn them on and off; it saves the change\n")
	out.WriteString("  # here. herdr.toast is separate: it is herdr's own notification of a handoff.\n")
	out.WriteString("  # While any is on, every open pull request is read this often.\n")
	_, _ = fmt.Fprintf(&out, "  interval: %s\n", cfg.Notifications.Interval)
	out.WriteString("  events:\n")
	for _, event := range NotificationEvents() {
		_, _ = fmt.Fprintf(&out, "    %s: %t\n", event, cfg.Notifications.Enabled(event))
	}
	out.WriteString("\n")

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
	out.WriteString("  roots: []\n\n")

	out.WriteString("security:\n")
	out.WriteString("  # Whose review feedback may be handed to an agent without asking you first.\n")
	out.WriteString("  # Feedback on a pull request with an untrusted participant is held instead,\n")
	out.WriteString("  # and W hands it over after a second press.\n")
	out.WriteString("  #\n")
	out.WriteString("  # GitHub authorAssociation values. MEMBER is not among the defaults: in a\n")
	out.WriteString("  # large organisation it implies no write access. Add it if yours is small.\n")
	writeSequence(&out, "trusted_associations", cfg.Security.TrustedAssociations)
	out.WriteString("  # Extra logins. A name ending in [bot] matches only a GitHub App, so a\n")
	out.WriteString("  # person who registers that name does not inherit its trust.\n")
	writeSequence(&out, "trusted_authors", cfg.Security.TrustedAuthors)
	out.WriteString("  # Hand work automatically only to agents their vendor's sandbox contains, and\n")
	out.WriteString("  # refuse to start one whose policy does not sandbox it. Applies to Claude Code\n")
	out.WriteString("  # today; W and F can still use an agent that is not sandboxed.\n")
	_, _ = fmt.Fprintf(&out, "  require_sandbox: %t\n", cfg.Security.RequireSandbox)

	return []byte(out.String())
}
