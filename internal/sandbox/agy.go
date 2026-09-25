package sandbox

// agy is Google Antigravity CLI. It has no profile yet: prutil starts it
// exactly as before, and security.require_sandbox does not apply to it.
//
// What the vendor documents, unverified here because the CLI is not installed
// on the machine this was built on: --sandbox turns on its sandbox-exec
// sandbox; policy lives in ~/.gemini/antigravity-cli/settings.json
// (enableTerminalSandbox, toolPermission "proceed-in-sandbox", read_url for the
// network allowlist, and no "unsandboxed" allow rules); and no command reports
// whether a session is sandboxed.
//
// Filling it in means a Launch returning the arguments (and writing any policy
// they need beneath env.Dir), and an Inspect that decides from a running
// agent's argv. docs/security/tier2-handoff.md is the brief, and
// claude.go is the worked example.
func agy() Profile {
	return Profile{Kind: "agy", Bin: "agy"}
}
