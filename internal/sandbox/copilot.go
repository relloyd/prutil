package sandbox

// copilot is GitHub Copilot CLI. It has no profile yet: prutil starts it
// exactly as before, and security.require_sandbox does not apply to it.
//
// What the vendor documents, unverified here because the CLI is not installed
// on the machine this was built on: --sandbox turns the sandbox on for one
// session; its policy lives under "sandbox" in ~/.copilot/settings.json (or
// wherever COPILOT_HOME points), with no launch-time policy file; file tools
// run inside Copilot itself and are not sandboxed, so protected paths need
// --deny-tool rules too; and no command reports whether a session is
// sandboxed.
//
// Filling it in means a Launch returning the arguments (and writing any policy
// they need beneath env.Dir), and an Inspect that decides from a running
// agent's argv. docs/security/tier2-handoff.md is the brief, and
// claude.go is the worked example.
func copilot() Profile {
	return Profile{Kind: "copilot", Bin: "copilot"}
}
