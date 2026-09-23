// Package handoff decides which coding agent should receive a pull request's
// review feedback, and sends it.
//
// The decision is not "any agent will do". An agent is a candidate only when
// git says its working directory is working on the pull request: prutil set
// the checkout up for it, the branch tracks or is named after the pull
// request's head, or the checkout holds the head commit. Branch names are never
// compared for a likeness, because people name branches in too many ways for a
// lookalike to mean the same work. What happens when no agent qualifies is
// herdr.fallback's to decide: new sets a workspace up, none says so and names
// the agents it passed over, and repo hands the work to any agent in the
// repository with the mismatch spelled out in the prompt.
package handoff

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/relloyd/prutil/internal/git"
	"github.com/relloyd/prutil/internal/herdr"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// Failures a caller has to phrase differently from one another.
var (
	// ErrNoAgent means no agent herdr knows about is working on the pull
	// request, either because none is in its repository or because the ones
	// that are have other work checked out.
	ErrNoAgent = errors.New("no agent is working on this pull request")
	// ErrBlocked means the target agent is sitting at an approval or question
	// dialog. Answering somebody else's dialog is not prutil's business.
	ErrBlocked = errors.New("the agent is waiting on a dialog of its own")
	// ErrStillWorking means the target never settled inside the configured
	// wait.
	ErrStillWorking = errors.New("the agent is still working")
	// ErrPromptNotAccepted means the target agent never entered a working or
	// blocked state after prompt submission.
	ErrPromptNotAccepted = errors.New("the agent did not accept the prompt")
	// ErrAgentKindRequired means a manual handoff needs to create an agent but
	// the configuration deliberately does not name a concrete agent kind.
	ErrAgentKindRequired = errors.New("herdr.agent_kind is required to start a new agent")
)

const (
	// provisionGrace gives an agent prutil has just started a moment to set
	// its terminal up before the prompt arrives.
	provisionGrace = 2 * time.Second
	// promptCheckInterval and promptAcceptTimeout are how often, and for how
	// long, prutil watches a freshly started agent for a sign that it took the
	// prompt.
	promptCheckInterval = 250 * time.Millisecond
	promptAcceptTimeout = 3 * time.Second
	// maxPromptAttempts bounds the submissions to a starting agent.
	maxPromptAttempts = 3
)

// Identifier reports what a directory holds, and whether the commit checked out
// there has another commit in its history.
type Identifier interface {
	Identify(ctx context.Context, dir string) git.Checkout
	Contains(ctx context.Context, dir, commit string) bool
}

// RepositoryResolver finds the local checkout where a manual handoff can
// create a worktree.
type RepositoryResolver interface {
	Resolve(ctx context.Context, repo string) (git.Checkout, error)
}

// Request is one pull request's feedback, ready to be handed over.
type Request struct {
	PR model.PullRequest
	// UnresolvedCount is every unresolved review thread on the pull request,
	// and NewCount how many of those prutil has not handed off before.
	UnresolvedCount int
	NewCount        int
	// Threads maps each unresolved review thread id to the id of its newest
	// comment, which is what the caller records once the handoff lands.
	Threads map[string]string
	// AllowProvision means the reader explicitly pressed W and permits a
	// no-agent handoff to create a worktree and start an agent.
	AllowProvision bool
	// CheckHandoff makes this a failed-check investigation rather than review
	// feedback. It uses the separate check prompt and carries all failures.
	CheckHandoff bool          // use the failed-check investigation prompt
	HeadOID      string        // commit whose checks are being investigated
	Checks       []model.Check // all failed checks sent together for correlation
}

// headOID is the commit the handoff is about: the pull request's head, or the
// commit a failed-check handoff named when the pull request it came with did
// not say.
func (r Request) headOID() string {
	if r.PR.HeadOID != "" {
		return r.PR.HeadOID
	}
	return r.HeadOID
}

// Result is what became of a handoff, in the shape the log wants.
type Result struct {
	Outcome string
	Target  string
	Kind    string
	Dir     string
	Prompt  string
	Detail  string
	// Provisioned says the result came from a workspace prutil created or
	// opened for the handoff, rather than an agent it found already running.
	Provisioned bool
	Workspace   string
	Tab         string
	// Waited is how long prutil spent waiting for a working agent to settle.
	Waited time.Duration
}

// Dispatcher sends pull request feedback to a coding agent.
type Dispatcher struct {
	herdr    herdr.Controller
	git      Identifier
	repos    RepositoryResolver
	fetch    git.PullRequestFetcher
	cfg      home.HerdrConfig
	selfPane string
	idleGap  time.Duration
	sleep    func(ctx context.Context, d time.Duration) bool
}

// Options wires a dispatcher to its collaborators.
type Options struct {
	Herdr herdr.Controller
	Git   Identifier
	// Repos locates a local checkout to provision only after a manual handoff
	// found no existing agent.
	Repos RepositoryResolver
	// Fetch reads a pull request head into the local branch a new worktree
	// uses. It is separate from Git so tests can make no process calls.
	Fetch git.PullRequestFetcher
	// Config supplies the prompt, the agent kind and the wait budget from
	// Herdr, and the agent polling gap from Watch.
	Config home.Config
	// SelfPane is the herdr pane prutil is itself running in, which must never
	// be handed work: that pane is the reader's, and on a good day it is the
	// terminal they are watching prutil in.
	SelfPane string
	// Sleep waits, reporting false when the context ended first. Tests replace
	// it so that a fifteen minute wait costs nothing.
	Sleep func(ctx context.Context, d time.Duration) bool
}

// New builds a dispatcher.
func New(opts Options) *Dispatcher {
	sleep := opts.Sleep
	if sleep == nil {
		sleep = wait
	}
	return &Dispatcher{
		herdr:    opts.Herdr,
		git:      opts.Git,
		repos:    opts.Repos,
		fetch:    opts.Fetch,
		cfg:      opts.Config.Herdr,
		selfPane: opts.SelfPane,
		idleGap:  opts.Config.Watch.IdleInterval.Duration(),
		sleep:    sleep,
	}
}

// DryRun reports whether handoffs are being recorded rather than sent.
func (d *Dispatcher) DryRun() bool { return d.cfg.DryRun }

// Dispatch finds the agent for a pull request and gives it the work. It always
// returns a Result worth logging, error or not, because "prutil could not find
// anybody to tell" is exactly the thing the log exists to record.
func (d *Dispatcher) Dispatch(ctx context.Context, req Request) (Result, error) {
	agents, err := d.herdr.Agents(ctx)
	if err != nil {
		return Result{Outcome: home.OutcomeFailed, Detail: err.Error()}, err
	}

	pr := req.PR
	pr.HeadOID = req.headOID()

	best, passed, found := d.pick(ctx, agents, pr)
	if !found {
		if req.AllowProvision || (d.cfg.Fallback == home.FallbackNew && d.canProvision()) {
			return d.provision(ctx, agents, req)
		}
		err := noAgent(pr, passed)
		res := Result{Outcome: home.OutcomeNoAgent, Detail: err.Error()}
		d.toast(ctx, req, err.Error())
		return res, err
	}

	agent := best.agent
	res := Result{Target: agent.Target(), Kind: agent.Kind, Dir: agent.Dir()}
	return d.send(ctx, req, agent, best.note(pr), res, false)
}

// send settles an agent, renders the prompt and submits it. initial carries
// workspace metadata when the agent was created for this handoff, and fresh
// says that it was: an agent prutil has only just started is the one that can
// miss the keystrokes, and so the only one prutil ever prompts twice.
func (d *Dispatcher) send(ctx context.Context, req Request, agent herdr.Agent, note string, initial Result, fresh bool) (Result, error) {
	res := initial
	settled, waited, err := d.settle(ctx, agent)
	res.Waited = waited
	if err != nil {
		// Only an agent that really is sitting at a dialog is recorded as
		// blocked. A wait that ran out, or a context that ended under it, is
		// an ordinary failure and reads as one in the log.
		res.Outcome = home.OutcomeFailed
		res.Detail = err.Error()
		switch {
		case errors.Is(err, ErrBlocked):
			res.Outcome = home.OutcomeBlocked
		case errors.Is(err, ErrNoAgent):
			res.Outcome = home.OutcomeNoAgent
		}
		d.toast(ctx, req, res.Detail)
		return res, err
	}
	res.Target = settled.Target()
	if settled.Kind != "" {
		res.Kind = settled.Kind
	}
	if settled.Dir() != "" {
		res.Dir = settled.Dir()
	}

	text, err := d.renderPrompt(req, note)
	if err != nil {
		// No toast, for the same reason as the dry run below: a prompt that
		// will not render is a configuration mistake that would otherwise
		// notify on every handoff until it was fixed.
		res.Outcome, res.Detail = home.OutcomeFailed, err.Error()
		return res, err
	}
	res.Prompt = text

	if d.cfg.DryRun {
		res.Outcome = home.OutcomeDryRun
		return res, nil
	}

	if err := d.submitPrompt(ctx, settled, text, fresh); err != nil {
		res.Detail = err.Error()
		res.Outcome = home.OutcomeFailed
		switch {
		case errors.Is(err, ErrBlocked):
			res.Outcome = home.OutcomeBlocked
		case errors.Is(err, ErrNoAgent):
			res.Outcome = home.OutcomeNoAgent
		}
		d.toast(ctx, req, res.Detail)
		return res, err
	}

	res.Outcome = home.OutcomeSent
	d.toast(ctx, req, fmt.Sprintf("sent to %s in %s", agentLabel(settled), short(res.Dir)))
	return res, nil
}

// submitPrompt gives the prompt to the agent.
//
// A freshly started agent is the one case where keystrokes go missing: its
// terminal may still be setting itself up when the text arrives. Only for that
// agent does prutil check that the prompt landed and submit again if it did
// not. An agent that was already running is prompted once, because herdr's
// status can lag a prompt that did arrive, and a second copy of the same
// feedback is worse than a slow status.
//
// Once any submission has succeeded the work is with the agent, so nothing
// after that turns the handoff into a failure.
func (d *Dispatcher) submitPrompt(ctx context.Context, agent herdr.Agent, text string, fresh bool) error {
	before := agent.Status
	if before == "" {
		before = herdr.StatusIdle
	}

	delivered := false
	for attempt := 1; attempt <= maxPromptAttempts; attempt++ {
		err := d.herdr.Prompt(ctx, agent.Target(), text)
		switch {
		case err == nil:
			delivered = true
		case delivered:
			// An earlier attempt reached the agent, so the work is there
			// whatever this one ran into, including a dialog the agent has
			// opened since.
			return nil
		case herdr.Code(err) == herdr.CodeAgentBlocked:
			// herdr refuses a submission to an agent sitting at a dialog
			// rather than answering it, which is a reason to try later.
			return fmt.Errorf("%w: %w", ErrBlocked, err)
		case herdr.Code(err) == herdr.CodeAgentNotFound:
			return fmt.Errorf("%w: %w", ErrNoAgent, err)
		case fresh && notReady(err) && attempt < maxPromptAttempts:
			// herdr says the agent is not listening yet, which is the one
			// refusal worth waiting out.
			if !d.sleep(ctx, promptBackoff(attempt)) {
				return err
			}
			continue
		default:
			return err
		}

		if !fresh {
			return nil
		}
		if accepted, known := d.accepted(ctx, agent.Target(), before); accepted || !known {
			// An unknown answer is not a reason to send the prompt twice.
			return nil
		}
		if attempt < maxPromptAttempts && !d.sleep(ctx, promptBackoff(attempt)) {
			// The handoff ran out of time, but the agent has the prompt.
			return nil
		}
	}
	return ErrPromptNotAccepted
}

// accepted reports whether the agent reacted to a prompt, and whether herdr
// could say at all.
//
// Reacting means working or waiting on a dialog of its own, or at least moving
// to some other state. "Not idle" would not do: done is a resting state, like
// idle, for an agent that has finished earlier work, so an agent sitting at
// done would look as though it had taken a prompt that never arrived.
func (d *Dispatcher) accepted(ctx context.Context, target, before string) (accepted, known bool) {
	var waited time.Duration
	for {
		agent, err := d.herdr.Agent(ctx, target)
		if err != nil {
			return false, false
		}
		switch {
		case agent.Status == herdr.StatusWorking || agent.Status == herdr.StatusBlocked:
			return true, true
		case agent.Status != before && agent.Status != herdr.StatusUnknown && agent.Status != "":
			return true, true
		case waited >= promptAcceptTimeout:
			return false, true
		}
		if !d.sleep(ctx, promptCheckInterval) {
			return false, false
		}
		waited += promptCheckInterval
	}
}

// notReady reports whether herdr refused a prompt because the agent has not
// finished starting up, which is the one refusal worth trying again.
func notReady(err error) bool {
	code := herdr.Code(err)
	return code == herdr.CodeAgentNotReady || code == herdr.CodeAgentPromptStalled
}

// promptBackoff spaces out the attempts to reach an agent that is still
// starting: a second, then two.
func promptBackoff(attempt int) time.Duration {
	return time.Duration(1<<(attempt-1)) * time.Second
}

// renderPrompt renders the prompt for a handoff and makes sure it carries the
// note. A template written before notes existed would drop it, and so would the
// copy of an older default that prutil saved into the configuration file on
// first run, yet the note is the one part of the prompt the agent cannot work
// out for itself.
func (d *Dispatcher) renderPrompt(req Request, note string) (string, error) {
	data := promptData(req, note)
	render := d.cfg.RenderPrompt
	if req.CheckHandoff {
		render = d.cfg.RenderCheckPrompt
	}
	text, err := render(data)
	if err != nil {
		return "", err
	}
	return withNote(text, note), nil
}

// withNote makes sure a rendered prompt carries the note as a paragraph of its
// own. It is appended when the template has nowhere for it. Spaces or tabs in
// front of it on its line are dropped, because an indented paragraph after a
// blank line is a Markdown code block, which an agent reads as a quotation
// rather than as something it has been told, and prompts saved into
// configuration files before the default stopped indenting it still do.
func withNote(text, note string) string {
	if note == "" {
		return text
	}
	at := strings.Index(text, note)
	if at < 0 {
		return text + "\n\n" + note
	}
	start := at
	for start > 0 && (text[start-1] == ' ' || text[start-1] == '\t') {
		start--
	}
	if start > 0 && text[start-1] != '\n' {
		// The note follows other words on its line, which is where the
		// template meant it to be.
		return text
	}
	return text[:start] + text[at:]
}

// fail records a handoff that did not happen, keeping whatever the caller had
// already learned about where it was going, and tells the reader. Every failure
// past the first herdr call goes through it, so that the paths which
// deliberately stay quiet are the ones that look unusual.
func (d *Dispatcher) fail(ctx context.Context, req Request, res Result, err error) (Result, error) {
	res.Outcome, res.Detail = home.OutcomeFailed, err.Error()
	d.toast(ctx, req, res.Detail)
	return res, err
}

// startAgentTimeout allows herdr enough time to recognise a new agent without
// holding the handoff open for its full work duration.
const startAgentTimeout = time.Minute

func (d *Dispatcher) canProvision() bool {
	return strings.TrimSpace(d.cfg.AgentKind) != "" && d.repos != nil && d.fetch != nil
}

// provision creates or reopens a herdr worktree for a handoff that found no
// agent working on the pull request. W always allows it; every other handoff
// reaches it only when herdr.fallback is new.
func (d *Dispatcher) provision(ctx context.Context, agents []herdr.Agent, req Request) (Result, error) {
	if strings.TrimSpace(d.cfg.AgentKind) == "" {
		return d.fail(ctx, req, Result{}, ErrAgentKindRequired)
	}
	if d.repos == nil || d.fetch == nil {
		return d.fail(ctx, req, Result{}, errors.New("workspace provisioning is not configured"))
	}

	checkout, err := d.repos.Resolve(ctx, req.PR.Repo)
	if err != nil {
		return d.fail(ctx, req, Result{}, err)
	}
	if checkout.Root == "" {
		return d.fail(ctx, req, Result{}, errors.New("the local checkout has no repository root"))
	}

	name, err := agentName(req.PR, agents)
	if err != nil {
		return d.fail(ctx, req, Result{}, err)
	}
	label := req.PR.Key().String()
	res := Result{
		Target: name,
		Kind:   d.cfg.AgentKind,
		Dir:    checkout.Root,
	}
	if d.cfg.DryRun {
		text, err := d.renderPrompt(req, "")
		if err != nil {
			// No toast. A prompt that will not render is a mistake in the
			// configuration file, which the reader fixes by looking at the
			// screen they are already looking at, and which would otherwise
			// produce a notification on every handoff until they did.
			res.Outcome, res.Detail = home.OutcomeFailed, err.Error()
			return res, err
		}
		res.Outcome = home.OutcomeDryRun
		res.Prompt = text
		res.Detail = "would create a worktree and start a new agent"
		return res, nil
	}

	session, reused, err := d.openWorktree(ctx, checkout.Root, workspaceBranch(req.PR), label, req.PR.Number)
	if err != nil {
		return d.fail(ctx, req, res, err)
	}
	res.Workspace, res.Tab = session.WorkspaceID, session.TabID
	res.Provisioned = true

	// A worktree prutil made earlier is reopened as it was left, without a
	// fetch: git refuses to fetch into a branch that is checked out. The agent
	// starting there is told where the pull request's commits are instead.
	note := ""
	if head := req.headOID(); reused != "" && head != "" && !d.git.Contains(ctx, reused, head) {
		note = staleWorkspaceNote(short(reused), head, req.PR.Number)
	}

	agent, err := d.herdr.StartAgent(ctx, name, d.cfg.AgentKind, session.RootPaneID, startAgentTimeout)
	if err != nil {
		return d.fail(ctx, req, res, err)
	}
	if agent.Target() == "" {
		return d.fail(ctx, req, res, errors.New("herdr started an agent without a target"))
	}

	if !d.sleep(ctx, provisionGrace) {
		return d.fail(ctx, req, res, ctx.Err())
	}

	return d.send(ctx, req, agent, note, res, true)
}

// openWorktree reuses an existing matching checkout before asking herdr to
// create one, and reports the path when it reused one, which is what lets the
// caller warn an agent about a workspace left behind the pull request. If
// creation reports an error after creating the checkout, a fresh list can
// safely recover only an exact branch match; otherwise the original error
// remains visible instead of guessing at a path.
func (d *Dispatcher) openWorktree(ctx context.Context, root, branch, label string, number int) (herdr.WorktreeSession, string, error) {
	worktrees, err := d.herdr.Worktrees(ctx, root)
	if err != nil {
		return herdr.WorktreeSession{}, "", err
	}
	if existing, ok := worktreeForBranch(worktrees, branch); ok {
		session, err := d.herdr.OpenWorktree(ctx, root, existing.Path, branch, label)
		return session, existing.Path, err
	}
	if err := d.fetch.FetchPullRequest(ctx, root, number, branch); err != nil {
		return herdr.WorktreeSession{}, "", err
	}

	session, createErr := d.herdr.CreateWorktree(ctx, root, branch, label)
	if createErr == nil {
		return session, "", nil
	}

	worktrees, listErr := d.herdr.Worktrees(ctx, root)
	if listErr != nil {
		return herdr.WorktreeSession{}, "", createErr
	}
	if existing, ok := worktreeForBranch(worktrees, branch); ok {
		session, err := d.herdr.OpenWorktree(ctx, root, existing.Path, branch, label)
		return session, existing.Path, err
	}
	return herdr.WorktreeSession{}, "", createErr
}

// worktreeForBranch returns a usable existing worktree for branch.
func worktreeForBranch(worktrees []herdr.Worktree, branch string) (herdr.Worktree, bool) {
	for _, worktree := range worktrees {
		if worktree.Branch == branch && strings.TrimSpace(worktree.Path) != "" {
			return worktree, true
		}
	}
	return herdr.Worktree{}, false
}

// workspaceBranch is a collision-resistant local ref owned by prutil. It
// deliberately differs from the pull request's head ref, which may be checked
// out or contain a user's divergent work in another worktree.
func workspaceBranch(pr model.PullRequest) string {
	return fmt.Sprintf("prutil/%s-%d", agentStem(pr.Repo), pr.Number)
}

// promptData builds the shared prompt template input.
//
// Everything here beyond prutil's own wording arrives from GitHub, and much of
// it is written by somebody other than the reader, so nothing reaches a
// template as it came. See model.SafeLine for what is taken out and why.
//
// Repo and Number are left alone: they are prutil's own key, matched against
// repoNamePattern before the search that produced them, and URL is the pull
// request's own, which is also what every check URL is measured against.
func promptData(req Request, note string) home.PromptData {
	return home.PromptData{
		Repo:            req.PR.Repo,
		Number:          req.PR.Number,
		URL:             req.PR.URL,
		Title:           model.SafeLine(req.PR.Title),
		HeadRef:         model.SafeLine(req.PR.HeadRef),
		BaseRef:         model.SafeLine(req.PR.BaseRef),
		UnresolvedCount: req.UnresolvedCount,
		NewCount:        req.NewCount,
		Note:            note,
		Checks:          safeChecks(req.Checks, req.PR.URL),
	}
}

// descriptionLimit caps a check's description in the prompt. A legacy status
// context's description is free text chosen by anything with commit-status
// write access, and the prompt lists every failed check, so without a cap one
// of them could be the whole prompt.
const descriptionLimit = 200

// safeChecks makes the failed-check list fit to be interpolated. A check's
// name and workflow come from a workflow file in the pull request's own head,
// which is the branch under review.
func safeChecks(checks []model.Check, prURL string) []model.Check {
	if len(checks) == 0 {
		return nil
	}
	out := make([]model.Check, 0, len(checks))
	for _, check := range checks {
		check.Name = model.SafeLine(check.Name)
		check.Workflow = model.SafeLine(check.Workflow)
		check.Description = model.ClipRunes(model.SafeLine(check.Description), descriptionLimit)
		check.URL = model.SameHostURL(check.URL, prURL)
		out = append(out, check)
	}
	return out
}

// agentName derives a valid, stable herdr name from the pull request and adds
// a short numeric suffix only when a live agent already holds that name.
func agentName(pr model.PullRequest, agents []herdr.Agent) (string, error) {
	used := make(map[string]bool, len(agents))
	for _, agent := range agents {
		if agent.Name != "" {
			used[agent.Name] = true
		}
	}

	stem := agentStem(pr.Repo)
	for attempt := 1; attempt <= 9999; attempt++ {
		suffix := fmt.Sprintf("-%d", pr.Number)
		if attempt > 1 {
			suffix += fmt.Sprintf("-%d", attempt)
		}
		limit := 32 - len("pr-") - len(suffix)
		if limit < 1 {
			return "", fmt.Errorf("could not name an agent for %s within herdr's limit", pr.Key())
		}
		name := "pr-" + stem[:min(len(stem), max(limit, 1))] + suffix
		if !used[name] {
			return name, nil
		}
	}
	return "", fmt.Errorf("could not find an unused herdr agent name for %s", pr.Key())
}

// agentStem reduces a repository name to the ASCII herdr agent-name alphabet.
func agentStem(repo string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(repo) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			out.WriteRune(r)
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '/' || r == '.' {
			out.WriteByte('-')
		}
	}
	stem := strings.Trim(out.String(), "-_")
	if stem == "" {
		return "review"
	}
	return stem
}

// candidate is an agent in the pull request's repository, and what git says
// about whether its checkout is working on the pull request.
type candidate struct {
	agent    herdr.Agent
	checkout git.Checkout
	// own says prutil set this checkout up for the pull request itself.
	own bool
	// onBranch says the branch tracks the pull request's head, or has its name.
	onBranch bool
	// hasHead says the checkout holds the pull request's head commit, perhaps
	// with commits of its own on top.
	hasHead bool
	score   int
}

// The evidence that a checkout is working on the pull request, and what each
// piece is worth. They add up, so a checkout prutil set up that also holds the
// latest commit outranks one that only has the right name, and being ready for
// input breaks a tie without ever standing in for evidence.
const (
	scoreOwn     = 4
	scoreBranch  = 2
	scoreHead    = 2
	scoreSettled = 1
)

// pick chooses the agent to hand the work to. It also returns the agents it
// passed over in the pull request's repository, so that a handoff nobody can
// take can say who was there and what they had checked out.
func (d *Dispatcher) pick(ctx context.Context, agents []herdr.Agent, pr model.PullRequest) (candidate, []candidate, bool) {
	var found, fallback, passed []candidate
	for _, agent := range agents {
		if agent.PaneID != "" && agent.PaneID == d.selfPane {
			continue
		}
		if d.cfg.AgentKind != "" && agent.Kind != d.cfg.AgentKind {
			continue
		}
		checkout := d.git.Identify(ctx, agent.Dir())
		if checkout.Repo == "" || !strings.EqualFold(checkout.Repo, pr.Repo) {
			continue
		}

		c := d.assess(ctx, agent, checkout, pr)
		if agent.Settled() {
			c.score += scoreSettled
		}
		switch {
		case c.score > scoreSettled:
			found = append(found, c)
		case d.cfg.Fallback == home.FallbackRepo:
			// Nothing but the repository matches. These are kept apart rather
			// than scored alongside, so that being ready for input can never
			// put an agent on other work above one on the pull request.
			fallback = append(fallback, c)
		default:
			passed = append(passed, c)
		}
	}
	if len(found) == 0 {
		found = fallback
	}
	if len(found) == 0 {
		return candidate{}, passed, false
	}

	// Sorted rather than scanned so that two equally good agents are picked
	// between the same way every time.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		return found[i].agent.PaneID < found[j].agent.PaneID
	})
	return found[0], passed, true
}

// assess asks git what one checkout in the pull request's repository is
// working on.
func (d *Dispatcher) assess(ctx context.Context, agent herdr.Agent, checkout git.Checkout, pr model.PullRequest) candidate {
	c := candidate{agent: agent, checkout: checkout}
	c.own = checkout.Branch != "" && checkout.Branch == workspaceBranch(pr)
	tracks := checkout.Upstream != "" &&
		(checkout.Upstream == "refs/heads/"+pr.HeadRef || checkout.Upstream == fmt.Sprintf("refs/pull/%d/head", pr.Number))
	c.onBranch = tracks || (checkout.Branch != "" && checkout.Branch == pr.HeadRef)

	if pr.HeadOID != "" && (checkout.Head == pr.HeadOID || d.git.Contains(ctx, agent.Dir(), pr.HeadOID)) {
		// A branch stacked on top of the pull request holds its head commit
		// too. When that branch tracks a remote branch of its own it is other
		// work, however much of this pull request it contains.
		c.hasHead = c.own || c.onBranch || checkout.Upstream == ""
	}

	if c.own {
		c.score += scoreOwn
	}
	if c.onBranch {
		c.score += scoreBranch
	}
	if c.hasHead {
		c.score += scoreHead
	}
	return c
}

// note is the warning the prompt carries about the checkout the work is going
// to. It only ever says something true and useful: that the checkout holds the
// pull request's work on a branch that will not reach it, or that it is on the
// right branch but behind. A checkout prutil set up for the pull request needs
// neither.
func (c candidate) note(pr model.PullRequest) string {
	dir, branch := short(c.agent.Dir()), branchLabel(c.checkout.Branch)
	switch {
	case c.own && (c.hasHead || pr.HeadOID == ""):
		return ""
	case c.own:
		return staleWorkspaceNote(dir, pr.HeadOID, pr.Number)
	case c.hasHead && !c.onBranch:
		return fmt.Sprintf(
			"The checkout in %s is on %s, which has this pull request's latest commit but is not its branch, %s. Make sure your commits reach %s.",
			dir, branch, pr.HeadRef, pr.HeadRef)
	case c.onBranch && !c.hasHead && pr.HeadOID != "":
		return fmt.Sprintf(
			"The checkout in %s is on %s but does not have this pull request's latest commit, %s. Pull before changing anything.",
			dir, branch, shortCommit(pr.HeadOID))
	case !c.hasHead && !c.onBranch && !c.own:
		return fmt.Sprintf(
			"The checkout in %s is on %s, not this pull request's %s. Switch to the right branch before changing anything.",
			dir, branch, pr.HeadRef)
	}
	return ""
}

// staleWorkspaceNote tells an agent in a workspace prutil made how to reach the
// pull request's latest commit. That branch tracks nothing, so pulling is not
// the instruction: the pull request's own ref is where the commits are.
func staleWorkspaceNote(dir, headOID string, number int) string {
	return fmt.Sprintf(
		"The checkout in %s does not have this pull request's latest commit, %s. "+
			"Run git fetch origin pull/%d/head && git merge --ff-only FETCH_HEAD before changing anything.",
		dir, shortCommit(headOID), number)
}

// noAgent explains a handoff nobody could take. Naming the agents that were in
// the repository, and what each had checked out, is what lets a reader who
// expected one of them see why it was passed over.
func noAgent(pr model.PullRequest, passed []candidate) error {
	if len(passed) == 0 {
		return fmt.Errorf("%w: none is checked out in %s", ErrNoAgent, pr.Repo)
	}
	seen := make([]string, 0, len(passed))
	for _, c := range passed {
		seen = append(seen, agentLabel(c.agent)+" on "+branchLabel(c.checkout.Branch))
	}
	return fmt.Errorf("%w: none in %s is on %s or has its latest commit; found %s",
		ErrNoAgent, pr.Repo, pr.HeadRef, strings.Join(seen, ", "))
}

// shortCommit abbreviates a commit hash the way git's own messages do.
func shortCommit(oid string) string {
	return oid[:min(len(oid), 7)]
}

// settle waits for a working agent to become ready for input, re-reading it
// over herdr's local socket rather than guessing from the listing prutil
// already has. It gives up at the configured budget, because a handoff that
// arrives an hour late is worse than one the reader is told about now.
func (d *Dispatcher) settle(ctx context.Context, agent herdr.Agent) (herdr.Agent, time.Duration, error) {
	switch {
	case agent.Status == herdr.StatusBlocked:
		return agent, 0, ErrBlocked
	case agent.Settled():
		return agent, 0, nil
	}

	budget := d.cfg.WaitForIdle.Duration()
	gap := d.idleGap
	if gap <= 0 {
		gap = 10 * time.Second
	}

	var waited time.Duration
	for waited < budget {
		if !d.sleep(ctx, gap) {
			return agent, waited, ctx.Err()
		}
		waited += gap

		fresh, err := d.herdr.Agent(ctx, agent.Target())
		if err != nil {
			// The agent can exit while prutil waits, which herdr reports as a
			// missing target. There is nobody left to hand the work to.
			if herdr.Code(err) == herdr.CodeAgentNotFound {
				return agent, waited, ErrNoAgent
			}
			return agent, waited, err
		}
		switch {
		case fresh.Status == herdr.StatusBlocked:
			return fresh, waited, ErrBlocked
		case fresh.Settled():
			return fresh, waited, nil
		}
	}
	return agent, waited, ErrStillWorking
}

// toastTimeout bounds the notification, which is a local socket call and has
// no business taking longer than a moment.
const toastTimeout = 5 * time.Second

// toast shows a herdr notification, on the assumption that by the time a
// handoff resolves the reader is looking at another workspace. Its own failure
// is not worth reporting: it is the consolation prize, not the outcome.
//
// It runs on a context of its own. The reason to show a toast is most often
// that the handoff failed, and the most common way for a handoff to fail is
// for its context to run out, which would take the toast with it.
func (d *Dispatcher) toast(ctx context.Context, req Request, body string) {
	title := fmt.Sprintf("%s: %d new review %s", req.PR.Key(), req.NewCount, plural(req.NewCount, "comment"))
	d.Notify(ctx, title, body)
}

// Notify shows a herdr notification for something the caller decided rather
// than something Dispatch did, under the same herdr.toast switch as the rest.
//
// Feedback prutil holds back never reaches Dispatch, so without this it would
// be the one outcome in the handoff log the reader is never told about, purely
// because of where the decision is made.
func (d *Dispatcher) Notify(ctx context.Context, title, body string) {
	if !d.cfg.Toast {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), toastTimeout)
	defer cancel()

	_ = d.herdr.Notify(ctx, title, body)
}

// wait sleeps for d, reporting false when the context ended first.
func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// agentLabel names an agent the way a status line should: its kind and where
// herdr put it.
func agentLabel(a herdr.Agent) string {
	if a.Name != "" {
		return a.Name
	}
	if a.Kind == "" {
		return a.Target()
	}
	return a.Kind + " " + a.Target()
}

// branchLabel names a branch, or says so when the head is detached.
func branchLabel(branch string) string {
	if branch == "" {
		return "no branch"
	}
	return branch
}

// short trims a home directory prefix off a path so that a status line spends
// its width on the part that identifies the checkout.
func short(path string) string {
	if home, err := userHome(); err == nil && home != "" && strings.HasPrefix(path, home+"/") {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// plural adds an s to word unless n is one.
func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
