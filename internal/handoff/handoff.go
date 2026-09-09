// Package handoff decides which coding agent should receive a pull request's
// review feedback, and sends it.
//
// The decision is not "any agent will do". An agent is a candidate only when
// its working directory is a checkout of the pull request's repository, and it
// is the right candidate when that checkout is also on the pull request's head
// branch. Anything less is still offered, with the mismatch spelled out in the
// prompt, because an agent one branch away is far more use than a toast.
package handoff

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/relloyd/prutil/internal/git"
	"github.com/relloyd/prutil/internal/herdr"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// Failures a caller has to phrase differently from one another.
var (
	// ErrNoAgent means no agent herdr knows about is checked out in the pull
	// request's repository.
	ErrNoAgent = errors.New("no agent is checked out in this repository")
	// ErrBlocked means the target agent is sitting at an approval or question
	// dialog. Answering somebody else's dialog is not prutil's business.
	ErrBlocked = errors.New("the agent is waiting on a dialog of its own")
	// ErrStillWorking means the target never settled inside the configured
	// wait.
	ErrStillWorking = errors.New("the agent is still working")
)

// Identifier reports what repository and branch a directory holds.
type Identifier interface {
	Identify(ctx context.Context, dir string) git.Checkout
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
}

// Result is what became of a handoff, in the shape the log wants.
type Result struct {
	Outcome string
	Target  string
	Kind    string
	Dir     string
	Prompt  string
	Detail  string
	// Waited is how long prutil spent waiting for a working agent to settle.
	Waited time.Duration
}

// Dispatcher sends pull request feedback to a coding agent.
type Dispatcher struct {
	herdr    herdr.Controller
	git      Identifier
	cfg      home.HerdrConfig
	selfPane string
	idleGap  time.Duration
	sleep    func(ctx context.Context, d time.Duration) bool
}

// Options wires a dispatcher to its collaborators.
type Options struct {
	Herdr herdr.Controller
	Git   Identifier
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

	agent, note, found := d.pick(ctx, agents, req.PR)
	if !found {
		res := Result{Outcome: home.OutcomeNoAgent, Detail: ErrNoAgent.Error()}
		d.toast(ctx, req, "no agent found in "+req.PR.Repo)
		return res, ErrNoAgent
	}

	res := Result{Target: agent.Target(), Kind: agent.Kind, Dir: agent.Dir()}

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
	res.Target, res.Kind, res.Dir = settled.Target(), settled.Kind, settled.Dir()

	text, err := d.cfg.RenderPrompt(home.PromptData{
		Repo:            req.PR.Repo,
		Number:          req.PR.Number,
		URL:             req.PR.URL,
		Title:           req.PR.Title,
		HeadRef:         req.PR.HeadRef,
		BaseRef:         req.PR.BaseRef,
		UnresolvedCount: req.UnresolvedCount,
		NewCount:        req.NewCount,
		Note:            note,
	})
	if err != nil {
		res.Outcome, res.Detail = home.OutcomeFailed, err.Error()
		return res, err
	}
	res.Prompt = text

	if d.cfg.DryRun {
		res.Outcome = home.OutcomeDryRun
		return res, nil
	}

	if err := d.herdr.Prompt(ctx, res.Target, text); err != nil {
		res.Detail = err.Error()
		// The agent can reach a dialog between the state prutil read and the
		// submission it sent, and herdr refuses the submission rather than
		// answering the dialog. That is a reason to try later, not a failure.
		res.Outcome = home.OutcomeFailed
		if herdr.Code(err) == herdr.CodeAgentBlocked {
			res.Outcome = home.OutcomeBlocked
		}
		d.toast(ctx, req, res.Detail)
		return res, err
	}

	res.Outcome = home.OutcomeSent
	d.toast(ctx, req, fmt.Sprintf("sent to %s in %s", agentLabel(settled), short(res.Dir)))
	return res, nil
}

// pick chooses the agent to hand the work to, and returns any warning the
// prompt should carry about the checkout it found.
func (d *Dispatcher) pick(ctx context.Context, agents []herdr.Agent, pr model.PullRequest) (herdr.Agent, string, bool) {
	type candidate struct {
		agent  herdr.Agent
		branch string
		score  int
	}

	var found []candidate
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

		// The right branch is worth more than the right repository, and an
		// agent ready for input is worth more than one part way through
		// something else.
		score := 1
		if checkout.Branch == pr.HeadRef {
			score = 3
		}
		if agent.Settled() {
			score++
		}
		found = append(found, candidate{agent: agent, branch: checkout.Branch, score: score})
	}
	if len(found) == 0 {
		return herdr.Agent{}, "", false
	}

	// Sorted rather than scanned so that two equally good agents are picked
	// between the same way every time.
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		return found[i].agent.PaneID < found[j].agent.PaneID
	})

	best := found[0]
	note := ""
	if best.branch != pr.HeadRef {
		note = fmt.Sprintf(
			"The checkout in %s is on %s, not this pull request's %s. Switch to the right branch before changing anything.",
			short(best.agent.Dir()), branchLabel(best.branch), pr.HeadRef)
	}
	return best.agent, note, true
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
	if !d.cfg.Toast {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), toastTimeout)
	defer cancel()

	title := fmt.Sprintf("%s: %d new review %s", req.PR.Key(), req.NewCount, plural(req.NewCount, "comment"))
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
