package ui

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/relloyd/prutil/internal/desktop"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

const (
	// settingsMaxWidth keeps the pane wide enough for settings names and values.
	settingsMaxWidth = 80
	// settingsDetailLines is how much of the selected setting's explanation is
	// shown beneath the list.
	settingsDetailLines = 3
	// settingsNoticeLines is the room the pane keeps for saying what just
	// happened, which can take a sentence and a hint.
	settingsNoticeLines = 2
	// settingsChrome is the lines every settings pane spends besides its rows
	// and its notice: top edge, rule above notice, the blank line that sets the
	// notice off from the bottom edge, and that edge. Without the blank one the
	// notice runs into the border, which carries its own hints, and the two
	// read as one line.
	settingsChrome = 4
	// settingsMinRows is how many rows the pane insists on showing before it
	// starts giving the explanation up. The list scrolls, so a pane with more
	// settings than fit is an ordinary pane rather than one that has run out
	// of room; only a genuinely short terminal has to choose.
	settingsMinRows = 6
)

// settingsMode is the interaction mode of the settings pane.
type settingsMode int

const (
	settingsModeNormal settingsMode = iota
	settingsModeEdit
	settingsModeTemplate
	settingsModeSubPane
)

// subPaneType names the active sub-pane for complex collections.
type subPaneType int

const (
	subPaneNone subPaneType = iota
	subPaneRepos
	subPaneReviewRepos
	subPaneDiscoveryRoots
	subPaneTrustedAssociations
	subPaneTrustedAuthors
)

// listSubPane describes a sub-pane over a sequence of strings.
//
// The sequences differ only in where they live, what one entry is called and
// what counts as a valid one, so they share the add, delete, list and render
// paths rather than each growing an arm in five switches. A map sub-pane is
// still its own case: two fields and a key are a different shape.
type listSubPane struct {
	title string
	// noun names one entry, for the prompt and for every notice about it.
	noun string
	path []string
	get  func(c *home.Config) []string
	set  func(c *home.Config, items []string)
	// clean normalises an entry the reader typed and rejects one that cannot
	// mean anything. Nil accepts whatever they wrote.
	clean func(entry string) (string, error)
}

// listSubPanes is every sequence the pane can manage.
func listSubPanes() map[subPaneType]listSubPane {
	return map[subPaneType]listSubPane{
		subPaneDiscoveryRoots: {
			title: "Discovery: Checkout Roots",
			noun:  "discovery root",
			path:  []string{"discovery", "roots"},
			get:   func(c *home.Config) []string { return c.Discovery.Roots },
			set:   func(c *home.Config, items []string) { c.Discovery.Roots = items },
		},
		subPaneTrustedAssociations: {
			title: "Security: Trusted Associations",
			noun:  "trusted association",
			path:  []string{"security", "trusted_associations"},
			get:   func(c *home.Config) []string { return c.Security.TrustedAssociations },
			set:   func(c *home.Config, items []string) { c.Security.TrustedAssociations = items },
			clean: cleanAssociation,
		},
		subPaneTrustedAuthors: {
			title: "Security: Trusted Authors",
			noun:  "trusted author",
			path:  []string{"security", "trusted_authors"},
			get:   func(c *home.Config) []string { return c.Security.TrustedAuthors },
			set:   func(c *home.Config, items []string) { c.Security.TrustedAuthors = items },
			clean: cleanAuthor,
		},
	}
}

// listSubPaneFor returns the sequence behind a sub-pane kind, if it is one.
func listSubPaneFor(kind subPaneType) (listSubPane, bool) {
	spec, ok := listSubPanes()[kind]
	return spec, ok
}

// associations are the authorAssociation values GitHub reports. A value
// outside them can never match, so it is a typo rather than a stricter policy,
// and saying so is better than silently trusting nobody.
var associations = []string{
	"OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR",
	"FIRST_TIME_CONTRIBUTOR", "FIRST_TIMER", "MANNEQUIN", "NONE",
}

// cleanAssociation upper-cases what the reader typed, since GitHub's values
// are upper-case, and refuses anything that is not one of them.
func cleanAssociation(entry string) (string, error) {
	got := strings.ToUpper(strings.TrimSpace(entry))
	if !slices.Contains(associations, got) {
		return "", fmt.Errorf("%q is not a GitHub author association; one of %s",
			entry, strings.Join(associations, ", "))
	}
	return got, nil
}

// cleanAuthor refuses anything that is not a plain login, optionally with the
// [bot] suffix that names a GitHub App. A login with a space in it is a typo,
// and a typo here reads as trusting nobody.
func cleanAuthor(entry string) (string, error) {
	got := strings.TrimSpace(entry)
	name := strings.TrimSuffix(got, "[bot]")
	if name == "" || strings.ContainsAny(name, " \t/@:") {
		return "", fmt.Errorf("%q is not a GitHub login; write it as it appears on github.com, "+
			"with [bot] on the end for an app", entry)
	}
	return got, nil
}

// subPaneState tracks state for managing maps or sequences.
type subPaneState struct {
	kind      subPaneType
	cursor    int
	offset    int
	adding    bool
	editing   bool
	keyInput  textinput.Model
	valInput  textinput.Model
	activeIdx int
}

// settingsPane is the s pane: the settings prutil can change for itself, each
// saved the moment it changes, so there is nothing to confirm and nothing to
// lose by closing it.
type settingsPane struct {
	open   bool
	keys   settingsKeyMap
	mode   settingsMode
	cursor int
	offset int

	// input is the inline text input for duration, int, and string fields.
	input textinput.Model

	// templateView tracks scrolling when previewing prompt templates.
	templateScroll int

	// subPane tracks state when editing maps or lists.
	subPane subPaneState

	// notice says what the last key did, and noticeErr whether it went wrong.
	notice    string
	noticeErr bool

	// unavailable is why a notification would not appear, asked once when the
	// pane opens.
	unavailable string
}

// settingsDisplayRow is either a section header or a setting row in the list.
type settingsDisplayRow struct {
	isHeader bool
	section  string
	itemIdx  int
}

// settingsLayout is where the pane sits and how its height is spent.
type settingsLayout struct {
	x, y          int
	width, height int
	inner         int
	window        int
	detail        bool
	noticeLines   int
}

// hinter is a notifier that can say where to look when a notification it
// showed did not appear.
type hinter interface {
	Hint() string
}

// templateEditorFinishedMsg is returned when an external $EDITOR exits.
type templateEditorFinishedMsg struct {
	tmpFile string
	isCheck bool
	err     error
}

// openSettings shows the settings pane with the first setting selected.
func (a *App) openSettings() {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.SetStyles(a.styles.helpInput())

	a.settings = settingsPane{
		open:  true,
		keys:  defaultSettingsKeys(),
		mode:  settingsModeNormal,
		input: ti,
	}
	if a.notifier == nil {
		a.settings.unavailable = "prutil cannot show desktop notifications here"
	} else if err := a.notifier.Available(); err != nil {
		a.settings.unavailable = err.Error()
	}
	a.clampSettingsScroll()
}

// closeSettings puts the pane away. Everything it changed is already saved.
func (a *App) closeSettings() {
	a.settings = settingsPane{}
}

// displayRows computes the flattened list of section headers and item rows.
func (a *App) displayRows() []settingsDisplayRow {
	items := allSettings()
	var rows []settingsDisplayRow
	curSec := ""
	for i, it := range items {
		if it.section != curSec {
			curSec = it.section
			rows = append(rows, settingsDisplayRow{isHeader: true, section: curSec})
		}
		rows = append(rows, settingsDisplayRow{isHeader: false, itemIdx: i})
	}
	return rows
}

// selectedRowIndex returns the index in displayRows() corresponding to s.cursor.
func (a *App) selectedRowIndex() int {
	rows := a.displayRows()
	for i, r := range rows {
		if !r.isHeader && r.itemIdx == a.settings.cursor {
			return i
		}
	}
	return 0
}

// updateSettings handles messages the pane takes for itself while open.
func (a *App) updateSettings(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case templateEditorFinishedMsg:
		return a.handleTemplateEditorFinished(msg), true
	case tea.KeyPressMsg:
		return a.handleSettingsKey(msg), true
	case tea.PasteMsg:
		if a.settings.mode == settingsModeEdit {
			var cmd tea.Cmd
			a.settings.input, cmd = a.settings.input.Update(msg)
			return cmd, true
		}
		return nil, true
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			a.moveSettings(-wheelStep)
		case tea.MouseWheelDown:
			a.moveSettings(wheelStep)
		}
		return nil, true
	case tea.MouseClickMsg:
		return a.clickSettings(msg), true
	}
	return nil, false
}

// handleSettingsKey applies a key press based on current settings mode.
func (a *App) handleSettingsKey(msg tea.KeyPressMsg) tea.Cmd {
	switch a.settings.mode {
	case settingsModeEdit:
		return a.handleEditKey(msg)
	case settingsModeTemplate:
		return a.handleTemplateKey(msg)
	case settingsModeSubPane:
		return a.handleSubPaneKey(msg)
	default:
		return a.handleNormalKey(msg)
	}
}

// handleNormalKey handles navigation, toggles, steppers, and mode transitions.
func (a *App) handleNormalKey(msg tea.KeyPressMsg) tea.Cmd {
	keys := a.settings.keys
	items := allSettings()
	if len(items) == 0 {
		return nil
	}
	item := items[a.settings.cursor]

	switch {
	case key.Matches(msg, keys.Quit):
		return tea.Quit
	case key.Matches(msg, keys.Close):
		a.closeSettings()
		return nil
	case key.Matches(msg, keys.Toggle):
		return a.handleToggleOrAction(item, msg)
	case key.Matches(msg, keys.Edit):
		return a.handleEditAction(item, msg)
	case key.Matches(msg, keys.CycleNext):
		return a.handleStepOrCycle(item, 1)
	case key.Matches(msg, keys.CyclePrev):
		return a.handleStepOrCycle(item, -1)
	case key.Matches(msg, keys.StepUp):
		return a.handleStepOrCycle(item, 1)
	case key.Matches(msg, keys.StepDown):
		return a.handleStepOrCycle(item, -1)
	case key.Matches(msg, keys.Default):
		return a.resetCurrentSetting(item)
	case key.Matches(msg, keys.NextSec):
		a.jumpSection(1)
		return nil
	case key.Matches(msg, keys.PrevSec):
		a.jumpSection(-1)
		return nil
	case key.Matches(msg, keys.Test):
		return a.testNotification()
	case key.Matches(msg, keys.Up):
		a.moveSettings(-1)
	case key.Matches(msg, keys.Down):
		a.moveSettings(1)
	case key.Matches(msg, keys.Top):
		a.settings.cursor = 0
		a.settings.notice, a.settings.noticeErr = "", false
		a.clampSettingsScroll()
	case key.Matches(msg, keys.Bottom):
		a.settings.cursor = len(items) - 1
		a.settings.notice, a.settings.noticeErr = "", false
		a.clampSettingsScroll()
	}
	return nil
}

// handleToggleOrAction executes space / x / enter toggle actions.
func (a *App) handleToggleOrAction(item settingDescriptor, msg tea.KeyPressMsg) tea.Cmd {
	switch item.kind {
	case settingKindBool:
		if item.toggle != nil {
			return item.toggle(a)
		}
	case settingKindEnum:
		if item.cycle != nil {
			return item.cycle(a, 1)
		}
	case settingKindTemplate:
		a.settings.mode = settingsModeTemplate
		a.settings.templateScroll = 0
		return nil
	case settingKindMap, settingKindList:
		return a.openSubPane(item)
	case settingKindDuration, settingKindInt, settingKindString:
		if msg.String() == "enter" {
			return a.startInlineEdit(item)
		}
	}
	return nil
}

// handleEditAction handles enter / e on setting items.
func (a *App) handleEditAction(item settingDescriptor, msg tea.KeyPressMsg) tea.Cmd {
	switch item.kind {
	case settingKindBool:
		if item.toggle != nil {
			return item.toggle(a)
		}
	case settingKindEnum:
		if item.cycle != nil {
			return item.cycle(a, 1)
		}
	case settingKindDuration, settingKindInt, settingKindString:
		return a.startInlineEdit(item)
	case settingKindTemplate:
		if msg.String() == "e" {
			return a.editTemplateInEditor(item.id == "herdr.check_prompt")
		}
		a.settings.mode = settingsModeTemplate
		a.settings.templateScroll = 0
		return nil
	case settingKindMap, settingKindList:
		return a.openSubPane(item)
	}
	return nil
}

// handleStepOrCycle steps numeric values or cycles enums.
func (a *App) handleStepOrCycle(item settingDescriptor, delta int) tea.Cmd {
	switch item.kind {
	case settingKindEnum:
		if item.cycle != nil {
			return item.cycle(a, delta)
		}
	case settingKindDuration, settingKindInt:
		if item.step != nil {
			return item.step(a, delta)
		}
	}
	return nil
}

// startInlineEdit switches the pane into inline text editing mode.
func (a *App) startInlineEdit(item settingDescriptor) tea.Cmd {
	a.settings.mode = settingsModeEdit
	a.settings.input.SetValue(item.getRaw(a))
	a.settings.input.CursorEnd()
	return a.settings.input.Focus()
}

// handleEditKey processes input during inline text editing.
func (a *App) handleEditKey(msg tea.KeyPressMsg) tea.Cmd {
	items := allSettings()
	item := items[a.settings.cursor]

	switch msg.String() {
	case "esc":
		a.settings.mode = settingsModeNormal
		a.settings.input.Blur()
		return nil
	case "enter":
		val := a.settings.input.Value()
		if item.saveInput != nil {
			if err := item.saveInput(a, val); err != nil {
				a.settings.setNotice("invalid value: "+err.Error(), true)
				return nil
			}
		}
		a.settings.mode = settingsModeNormal
		a.settings.input.Blur()
		return nil
	default:
		var cmd tea.Cmd
		a.settings.input, cmd = a.settings.input.Update(msg)
		return cmd
	}
}

// handleTemplateKey processes keys in the template viewer.
func (a *App) handleTemplateKey(msg tea.KeyPressMsg) tea.Cmd {
	items := allSettings()
	item := items[a.settings.cursor]
	isCheck := item.id == "herdr.check_prompt"

	switch msg.String() {
	case "esc", "q":
		a.settings.mode = settingsModeNormal
		return nil
	case "e", "enter":
		return a.editTemplateInEditor(isCheck)
	case "d":
		if item.reset != nil {
			_ = item.reset(a)
			a.settings.setNotice("template reset to default · saved", false)
		}
		return nil
	case "up", "k":
		if a.settings.templateScroll > 0 {
			a.settings.templateScroll--
		}
		return nil
	case "down", "j":
		a.settings.templateScroll++
		return nil
	}
	return nil
}

// editTemplateInEditor spawns $EDITOR for editing templates.
func (a *App) editTemplateInEditor(isCheck bool) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		for _, name := range []string{"nano", "vim", "vi"} {
			if path, err := exec.LookPath(name); err == nil {
				editor = path
				break
			}
		}
	}
	if editor == "" {
		a.settings.setNotice("no editor found ($EDITOR is not set)", true)
		return nil
	}

	content := a.homeCfg.Herdr.Prompt
	prefix := "prutil-prompt-*.tmpl"
	if isCheck {
		content = a.homeCfg.Herdr.CheckPrompt
		if content == "" {
			content = home.DefaultCheckPrompt
		}
		prefix = "prutil-check-prompt-*.tmpl"
	} else if content == "" {
		content = home.DefaultPrompt
	}

	tmp, err := os.CreateTemp("", prefix)
	if err != nil {
		a.settings.setNotice("could not create temporary file: "+err.Error(), true)
		return nil
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		a.settings.setNotice("could not write temporary file: "+err.Error(), true)
		return nil
	}
	_ = tmp.Close()

	parts := strings.Fields(editor)
	cmdName := parts[0]
	args := append(parts[1:], tmpName)
	c := exec.Command(cmdName, args...)

	return tea.ExecProcess(c, func(err error) tea.Msg {
		return templateEditorFinishedMsg{
			tmpFile: tmpName,
			isCheck: isCheck,
			err:     err,
		}
	})
}

// handleTemplateEditorFinished parses and saves template changes from $EDITOR.
func (a *App) handleTemplateEditorFinished(msg templateEditorFinishedMsg) tea.Cmd {
	defer func() { _ = os.Remove(msg.tmpFile) }()
	if msg.err != nil {
		a.settings.setNotice("editor failed: "+msg.err.Error(), true)
		return nil
	}
	data, err := os.ReadFile(msg.tmpFile)
	if err != nil {
		a.settings.setNotice("could not read edited template: "+err.Error(), true)
		return nil
	}
	newContent := strings.TrimRight(string(data), "\r\n")
	if msg.isCheck {
		if _, err := template.New("check_prompt").Parse(newContent); err != nil {
			a.settings.setNotice("invalid template syntax: "+err.Error(), true)
			return nil
		}
		if err := a.saveBlockScalar([]string{"herdr", "check_prompt"}, newContent, func(c *home.Config) {
			c.Herdr.CheckPrompt = newContent
		}); err != nil {
			a.settings.setNotice("could not save template: "+err.Error(), true)
			return nil
		}
		a.settings.setNotice("check prompt template updated · saved", false)
	} else {
		if _, err := template.New("prompt").Parse(newContent); err != nil {
			a.settings.setNotice("invalid template syntax: "+err.Error(), true)
			return nil
		}
		if err := a.saveBlockScalar([]string{"herdr", "prompt"}, newContent, func(c *home.Config) {
			c.Herdr.Prompt = newContent
		}); err != nil {
			a.settings.setNotice("could not save template: "+err.Error(), true)
			return nil
		}
		a.settings.setNotice("prompt template updated · saved", false)
	}
	return nil
}

// openSubPane initializes and opens the sub-pane for collections.
func (a *App) openSubPane(item settingDescriptor) tea.Cmd {
	var kind subPaneType
	switch item.id {
	case "repos":
		kind = subPaneRepos
	case "review.repos":
		kind = subPaneReviewRepos
	case "discovery.roots":
		kind = subPaneDiscoveryRoots
	case "security.trusted_associations":
		kind = subPaneTrustedAssociations
	case "security.trusted_authors":
		kind = subPaneTrustedAuthors
	}
	if kind == subPaneNone {
		return nil
	}

	ki := textinput.New()
	ki.Prompt = "key: "
	if _, isList := listSubPaneFor(kind); isList {
		// One field, and the row above it already names what goes in it.
		ki.Prompt = ""
	}
	ki.SetStyles(a.styles.helpInput())

	vi := textinput.New()
	vi.Prompt = "val: "
	vi.SetStyles(a.styles.helpInput())

	a.settings.mode = settingsModeSubPane
	a.settings.subPane = subPaneState{
		kind:     kind,
		cursor:   0,
		keyInput: ki,
		valInput: vi,
	}
	return nil
}

// handleSubPaneKey processes keys inside the maps/lists sub-pane.
func (a *App) handleSubPaneKey(msg tea.KeyPressMsg) tea.Cmd {
	sp := &a.settings.subPane
	if sp.adding || sp.editing {
		return a.handleSubPaneInputKey(msg)
	}

	keys := a.subPaneEntries()
	count := len(keys)

	switch msg.String() {
	case "esc", "q":
		a.settings.mode = settingsModeNormal
		return nil
	case "up", "k":
		if sp.cursor > 0 {
			sp.cursor--
		}
		return nil
	case "down", "j":
		if sp.cursor < count-1 {
			sp.cursor++
		}
		return nil
	case "a":
		sp.adding = true
		sp.editing = false
		sp.keyInput.SetValue("")
		sp.valInput.SetValue("")
		sp.activeIdx = 0
		return sp.keyInput.Focus()
	case "d", "x":
		if count > 0 && sp.cursor < count {
			return a.deleteSubPaneEntry(keys[sp.cursor])
		}
	}
	return nil
}

// handleSubPaneInputKey handles input fields while adding/editing entries.
func (a *App) handleSubPaneInputKey(msg tea.KeyPressMsg) tea.Cmd {
	sp := &a.settings.subPane
	switch msg.String() {
	case "esc":
		sp.adding = false
		sp.editing = false
		return nil
	case "tab":
		if _, isList := listSubPaneFor(sp.kind); !isList {
			sp.activeIdx = 1 - sp.activeIdx
			if sp.activeIdx == 0 {
				sp.valInput.Blur()
				return sp.keyInput.Focus()
			}
			sp.keyInput.Blur()
			return sp.valInput.Focus()
		}
	case "enter":
		if spec, isList := listSubPaneFor(sp.kind); isList {
			val := strings.TrimSpace(sp.valInput.Value())
			if val == "" {
				val = strings.TrimSpace(sp.keyInput.Value())
			}
			if val != "" {
				a.addListEntry(spec, val)
			}
			sp.adding = false
			return nil
		}

		// Map entry addition
		k := strings.TrimSpace(sp.keyInput.Value())
		v := strings.TrimSpace(sp.valInput.Value())
		if k != "" && v != "" {
			switch sp.kind {
			case subPaneRepos:
				if err := a.saveMapEntry([]string{"repos"}, k, v, func(c *home.Config) {
					if c.Repos == nil {
						c.Repos = map[string]string{}
					}
					c.Repos[k] = v
				}); err != nil {
					a.settings.setNotice("could not save the repository path: "+err.Error(), true)
					return nil
				}
				a.settings.setNotice(fmt.Sprintf("Saved repo path %s -> %s · saved", k, v), false)
			case subPaneReviewRepos:
				if err := a.saveMapEntry([]string{"review", "repos"}, k, v, func(c *home.Config) {
					if c.Review.Repos == nil {
						c.Review.Repos = map[string]string{}
					}
					c.Review.Repos[k] = v
				}); err != nil {
					a.settings.setNotice("could not save the review trigger: "+err.Error(), true)
					return nil
				}
				a.settings.setNotice(fmt.Sprintf("Saved review trigger for %s · saved", k), false)
			}
		}
		sp.adding = false
		return nil
	default:
		if sp.activeIdx == 0 {
			var cmd tea.Cmd
			sp.keyInput, cmd = sp.keyInput.Update(msg)
			return cmd
		}
		var cmd tea.Cmd
		sp.valInput, cmd = sp.valInput.Update(msg)
		return cmd
	}
	return nil
}

// subPaneEntries returns list of entry labels for active subPane.
func (a *App) subPaneEntries() []string {
	sp := a.settings.subPane
	switch sp.kind {
	case subPaneRepos:
		var res []string
		keys := slices.Sorted(maps.Keys(a.homeCfg.Repos))
		for _, k := range keys {
			res = append(res, fmt.Sprintf("%s → %s", k, a.homeCfg.Repos[k]))
		}
		return res
	case subPaneReviewRepos:
		var res []string
		keys := slices.Sorted(maps.Keys(a.homeCfg.Review.Repos))
		for _, k := range keys {
			res = append(res, fmt.Sprintf("%s → %s", k, a.homeCfg.Review.Repos[k]))
		}
		return res
	}
	if spec, isList := listSubPaneFor(sp.kind); isList {
		return spec.get(&a.homeCfg)
	}
	return nil
}

// addListEntry appends one entry to a sequence and saves the file, reporting
// what happened on the pane's own notice line.
func (a *App) addListEntry(spec listSubPane, entry string) {
	if spec.clean != nil {
		cleaned, err := spec.clean(entry)
		if err != nil {
			a.settings.setNotice(err.Error(), true)
			return
		}
		entry = cleaned
	}
	items := append([]string{}, spec.get(&a.homeCfg)...)
	if slices.ContainsFunc(items, func(got string) bool { return strings.EqualFold(got, entry) }) {
		a.settings.setNotice(fmt.Sprintf("%s %q is already listed", spec.noun, entry), true)
		return
	}
	items = append(items, entry)
	if err := a.saveSequence(spec.path, items, func(c *home.Config) { spec.set(c, items) }); err != nil {
		a.settings.setNotice("could not save the "+spec.noun+": "+err.Error(), true)
		return
	}
	a.settings.setNotice(fmt.Sprintf("Added %s %q · saved", spec.noun, entry), false)
}

// deleteSubPaneEntry removes the selected item from map or sequence.
func (a *App) deleteSubPaneEntry(entry string) tea.Cmd {
	sp := &a.settings.subPane
	key := entry
	if k, _, found := strings.Cut(entry, " → "); found {
		key = k
	}
	switch sp.kind {
	case subPaneRepos:
		if err := a.deleteMapEntry([]string{"repos"}, key, func(c *home.Config) {
			delete(c.Repos, key)
		}); err != nil {
			a.settings.setNotice("could not remove the repository path: "+err.Error(), true)
			return nil
		}
		a.settings.setNotice(fmt.Sprintf("Deleted repository path %q · saved", key), false)
	case subPaneReviewRepos:
		if err := a.deleteMapEntry([]string{"review", "repos"}, key, func(c *home.Config) {
			delete(c.Review.Repos, key)
		}); err != nil {
			a.settings.setNotice("could not remove the review trigger: "+err.Error(), true)
			return nil
		}
		a.settings.setNotice(fmt.Sprintf("Deleted review trigger for %q · saved", key), false)
	}
	if spec, isList := listSubPaneFor(sp.kind); isList {
		// Not a nil slice: ParseConfig reads "roots: []" back as an empty one,
		// so nil here would never equal what the file says and the save would
		// be refused for removing the last entry. That refusal used to be
		// discarded.
		kept := []string{}
		for _, got := range spec.get(&a.homeCfg) {
			if got != entry {
				kept = append(kept, got)
			}
		}
		if err := a.saveSequence(spec.path, kept, func(c *home.Config) { spec.set(c, kept) }); err != nil {
			a.settings.setNotice("could not save the "+spec.noun+": "+err.Error(), true)
			return nil
		}
		a.settings.setNotice(fmt.Sprintf("Deleted %s %q · saved", spec.noun, entry), false)
	}
	if sp.cursor > 0 && sp.cursor >= len(a.subPaneEntries()) {
		sp.cursor = len(a.subPaneEntries()) - 1
	}
	return nil
}

// resetCurrentSetting restores default value for current setting.
func (a *App) resetCurrentSetting(item settingDescriptor) tea.Cmd {
	if item.reset != nil {
		if err := item.reset(a); err != nil {
			a.settings.setNotice("could not reset setting: "+err.Error(), true)
			return nil
		}
	}
	a.settings.setNotice(fmt.Sprintf("%s reset to default · saved", item.title), false)
	return nil
}

// jumpSection moves the selection to the next or previous section.
func (a *App) jumpSection(delta int) {
	items := allSettings()
	curSec := items[a.settings.cursor].section
	if delta > 0 {
		for i := a.settings.cursor + 1; i < len(items); i++ {
			if items[i].section != curSec {
				a.settings.cursor = i
				a.settings.notice, a.settings.noticeErr = "", false
				a.clampSettingsScroll()
				return
			}
		}
	} else {
		// Jump to start of current section if not already there, else previous section
		firstInCurSec := a.settings.cursor
		for i := a.settings.cursor - 1; i >= 0; i-- {
			if items[i].section == curSec {
				firstInCurSec = i
			} else {
				break
			}
		}
		if a.settings.cursor > firstInCurSec {
			a.settings.cursor = firstInCurSec
		} else if firstInCurSec > 0 {
			prevSec := items[firstInCurSec-1].section
			for i := firstInCurSec - 1; i >= 0; i-- {
				if items[i].section == prevSec {
					a.settings.cursor = i
				} else {
					break
				}
			}
		}
		a.settings.notice, a.settings.noticeErr = "", false
		a.clampSettingsScroll()
	}
}

// clickSettings toggles or selects the setting a left click lands on.
func (a *App) clickSettings(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft {
		return nil
	}
	l := a.settingsLayout()
	row := msg.Y - l.y - 1
	if msg.X < l.x || msg.X >= l.x+l.width || row < 0 || row >= l.window {
		return nil
	}
	dispRows := a.displayRows()
	index := a.settings.offset + row
	if index >= len(dispRows) {
		return nil
	}
	target := dispRows[index]
	if target.isHeader {
		return nil
	}
	a.settings.cursor = target.itemIdx
	items := allSettings()
	return a.handleToggleOrAction(items[target.itemIdx], tea.KeyPressMsg{})
}

// moveSettings steps the selection by delta, clamped to the settings list.
func (a *App) moveSettings(delta int) {
	s := &a.settings
	items := allSettings()
	cursor := min(max(s.cursor+delta, 0), len(items)-1)
	if cursor != s.cursor {
		s.notice, s.noticeErr = "", false
	}
	s.cursor = cursor
	a.clampSettingsScroll()
}

// clampSettingsScroll keeps the selected display row inside the drawn window.
func (a *App) clampSettingsScroll() {
	s := &a.settings
	dispRows := a.displayRows()
	targetRow := a.selectedRowIndex()
	s.offset = clampOffset(s.offset, targetRow, a.settingsLayout().window, len(dispRows))
}

// setNotice replaces what the pane says about the last thing it did.
func (s *settingsPane) setNotice(text string, isErr bool) {
	s.notice, s.noticeErr = text, isErr
}

// testNotification shows a sample notification.
func (a *App) testNotification() tea.Cmd {
	if a.notifier == nil {
		a.settings.setNotice("prutil cannot show desktop notifications here", true)
		return nil
	}
	a.settings.setNotice("sending a test notification…", false)
	notifier := a.notifier
	return func() tea.Msg {
		return settingsTestMsg{err: notifier.Notify(desktop.Notification{
			Title: "Test notification",
			Body:  "This is how prutil will tell you that a pull request has changed.",
		})}
	}
}

// applySettingsTest reports how the test notification went.
func (a *App) applySettingsTest(msg settingsTestMsg) tea.Cmd {
	text, isErr := "sent a test notification", false
	if msg.err != nil {
		text, isErr = "the test notification failed: "+msg.err.Error(), true
	} else if h, ok := a.notifier.(hinter); ok && h.Hint() != "" {
		text += ". Nothing appeared? " + h.Hint()
	}
	if !a.settings.open {
		return status(text)
	}
	a.settings.setNotice(text, isErr)
	return nil
}

// settingsLayout sizes and places the pane for the current terminal.
func (a *App) settingsLayout() settingsLayout {
	l := settingsLayout{width: a.width, detail: true, noticeLines: settingsNoticeLines}
	room := a.height
	if a.floating() {
		l.width = min(a.width-4, settingsMaxWidth)
		room -= 2
	}
	l.inner = max(l.width-4, 1)

	dispRows := len(a.displayRows())
	fixed := func() int {
		n := settingsChrome + l.noticeLines
		if l.detail {
			n += 1 + settingsDetailLines
		}
		return n
	}
	// What gets given up when it will not all fit, in order. settingsChrome
	// counts the blank line above the bottom edge, but these two questions do
	// not: the blank comes out of the window instead, so that a row the reader
	// can scroll to is what pays for it rather than the explanation of the
	// setting they are sitting on.
	//
	// The same reasoning caps the rows these questions ask about. The list
	// scrolls, so needing more room than the terminal has is the ordinary
	// state of a pane with many settings, and measuring against all of them
	// would drop the explanation the moment one setting too many was added.
	// What matters is whether enough rows survive to navigate by.
	fits := func() bool { return fixed()-1+min(dispRows, settingsMinRows) <= room }
	if !fits() {
		l.detail = false
	}
	if !fits() {
		l.noticeLines = 1
	}
	l.window = max(min(dispRows, room-fixed()), 1)
	l.height = fixed() + l.window
	l.x = max((a.width-l.width)/2, 0)
	l.y = max((a.height-l.height)/2, 0)
	return l
}

// closeBox finishes a pane with the ending all three share: a blank row that
// sets the last line off from the bottom edge, and the edge itself carrying the
// pane's hints.
func (a *App) closeBox(box []string, l settingsLayout, hints string) []string {
	return append(box,
		a.frameRow("", l.inner),
		a.edge(l.width, "╰", "╯", hints, ""),
	)
}

// renderSettings draws the pane over a finished screen.
func (a *App) renderSettings(base []string) []string {
	l := a.settingsLayout()
	var box []string
	switch a.settings.mode {
	case settingsModeTemplate:
		box = a.templateBox(l)
	case settingsModeSubPane:
		box = a.subPaneBox(l)
	default:
		box = a.settingsBox(l)
	}
	return a.floatOver(base, box, l.x, l.y, l.width)
}

// templateBox renders the template preview modal.
func (a *App) templateBox(l settingsLayout) []string {
	s := &a.settings
	items := allSettings()
	item := items[s.cursor]
	isCheck := item.id == "herdr.check_prompt"

	title := "Template: " + item.title
	box := make([]string, 0, l.height)
	box = append(box,
		a.edge(l.width, "╭", "╮", a.styles.OverlayTitle.Render(title), a.styles.Muted.Render(a.configLabel())),
	)

	content := a.homeCfg.Herdr.Prompt
	if isCheck {
		content = a.homeCfg.Herdr.CheckPrompt
		if content == "" {
			content = home.DefaultCheckPrompt
		}
	} else if content == "" {
		content = home.DefaultPrompt
	}

	lines := strings.Split(content, "\n")
	numLines := len(lines)
	if s.templateScroll < 0 {
		s.templateScroll = 0
	}
	if s.templateScroll >= numLines && numLines > 0 {
		s.templateScroll = numLines - 1
	}

	for i := 0; i < l.window; i++ {
		lineIdx := s.templateScroll + i
		text := ""
		if lineIdx < numLines {
			lineNum := fmt.Sprintf("%2d │ ", lineIdx+1)
			text = "  " + a.styles.Muted.Render(lineNum) + a.styles.Text.Render(lines[lineIdx])
		}
		box = append(box, a.frameRow(text, l.inner))
	}

	if l.detail {
		box = append(box, a.frameRule(l.width))
		detail := item.detail + " Press 'e' or 'enter' to launch your $EDITOR."
		dLines := wrapLines(detail, l.inner, settingsDetailLines)
		for i := 0; i < settingsDetailLines; i++ {
			text := ""
			if i < len(dLines) {
				text = dLines[i]
			}
			box = append(box, a.frameRow(a.styles.Muted.Render(text), l.inner))
		}
	}

	box = append(box, a.frameRule(l.width))
	notice, style := a.settingsNotice()
	nLines := wrapLines(notice, l.inner, l.noticeLines)
	for i := 0; i < l.noticeLines; i++ {
		line := ""
		if i < len(nLines) {
			line = style.Render(nLines[i])
		}
		box = append(box, a.frameRow(line, l.inner))
	}

	hints := a.styles.OverlayKey.Render("e") + " " + a.styles.Muted.Render("edit in $EDITOR") +
		a.styles.Muted.Render(" · ") + a.styles.OverlayKey.Render("d") + " " + a.styles.Muted.Render("default") +
		a.styles.Muted.Render(" · ") + a.styles.OverlayKey.Render("↑↓") + " " + a.styles.Muted.Render("scroll") +
		a.styles.Muted.Render(" · ") + a.styles.OverlayKey.Render("esc") + " " + a.styles.Muted.Render("back")

	return a.closeBox(box, l, hints)
}

// subPaneBox renders the modal for collections (repos, review.repos, discovery.roots).
func (a *App) subPaneBox(l settingsLayout) []string {
	sp := &a.settings.subPane
	title := "Manage Items"
	switch sp.kind {
	case subPaneRepos:
		title = "Repositories: Explicit Paths"
	case subPaneReviewRepos:
		title = "Review: Repository Overrides"
	}
	if spec, isList := listSubPaneFor(sp.kind); isList {
		title = spec.title
	}

	box := make([]string, 0, l.height)
	box = append(box,
		a.edge(l.width, "╭", "╮", a.styles.OverlayTitle.Render(title), a.styles.Muted.Render(a.configLabel())),
	)

	if sp.adding {
		box = append(box, a.frameRow("  "+a.styles.SectionHdr.Render("ADD NEW ENTRY"), l.inner))
		if spec, isList := listSubPaneFor(sp.kind); isList {
			prompt := "  " + capitalise(spec.noun) + ": " + sp.keyInput.View()
			box = append(box, a.frameRow(prompt, l.inner))
		} else {
			p1 := "  Repo (owner/repo): " + sp.keyInput.View()
			box = append(box, a.frameRow(p1, l.inner))
			p2 := "  Value / Comment:   " + sp.valInput.View()
			box = append(box, a.frameRow(p2, l.inner))
		}
		for i := len(box) - 1; i < l.window; i++ {
			box = append(box, a.frameRow("", l.inner))
		}
	} else {
		entries := a.subPaneEntries()
		if len(entries) == 0 {
			sp.cursor = 0
			sp.offset = 0
			box = append(box, a.frameRow("  "+a.styles.Muted.Render("(no entries configured — press 'a' to add)"), l.inner))
			for i := 1; i < l.window; i++ {
				box = append(box, a.frameRow("", l.inner))
			}
		} else {
			if sp.cursor < 0 {
				sp.cursor = 0
			}
			if sp.cursor >= len(entries) {
				sp.cursor = len(entries) - 1
			}
			if sp.cursor < sp.offset {
				sp.offset = sp.cursor
			}
			if sp.cursor >= sp.offset+l.window {
				sp.offset = sp.cursor - l.window + 1
			}
			for i := 0; i < l.window; i++ {
				idx := sp.offset + i
				text := ""
				if idx < len(entries) {
					prefix, style := "  ", a.styles.Text
					if idx == sp.cursor {
						prefix, style = a.styles.SelectBar.Render("▌")+" ", a.styles.Title
					}
					text = prefix + style.Render(entries[idx])
				}
				box = append(box, a.frameRow(text, l.inner))
			}
		}
	}

	if l.detail {
		box = append(box, a.frameRule(l.width))
		detail := "Manage configured entries. Changes are saved immediately to config.yaml."
		dLines := wrapLines(detail, l.inner, settingsDetailLines)
		for i := 0; i < settingsDetailLines; i++ {
			text := ""
			if i < len(dLines) {
				text = dLines[i]
			}
			box = append(box, a.frameRow(a.styles.Muted.Render(text), l.inner))
		}
	}

	box = append(box, a.frameRule(l.width))
	text, style := a.settingsNotice()
	nLines := wrapLines(text, l.inner, l.noticeLines)
	for i := 0; i < l.noticeLines; i++ {
		line := ""
		if i < len(nLines) {
			line = style.Render(nLines[i])
		}
		box = append(box, a.frameRow(line, l.inner))
	}

	var hints string
	if sp.adding {
		hints = a.styles.OverlayKey.Render("enter") + " " + a.styles.Muted.Render("save") +
			a.styles.Muted.Render(" · ") + a.styles.OverlayKey.Render("tab") + " " + a.styles.Muted.Render("next field") +
			a.styles.Muted.Render(" · ") + a.styles.OverlayKey.Render("esc") + " " + a.styles.Muted.Render("cancel")
	} else {
		hints = a.styles.OverlayKey.Render("a") + " " + a.styles.Muted.Render("add") +
			a.styles.Muted.Render(" · ") + a.styles.OverlayKey.Render("d/x") + " " + a.styles.Muted.Render("delete") +
			a.styles.Muted.Render(" · ") + a.styles.OverlayKey.Render("↑↓") + " " + a.styles.Muted.Render("select") +
			a.styles.Muted.Render(" · ") + a.styles.OverlayKey.Render("esc") + " " + a.styles.Muted.Render("back")
	}

	return a.closeBox(box, l, hints)
}

// settingsBox draws the pane container and its content lines.
func (a *App) settingsBox(l settingsLayout) []string {
	s := &a.settings
	box := make([]string, 0, l.height)
	box = append(box,
		a.edge(l.width, "╭", "╮", a.styles.OverlayTitle.Render("Settings"), a.styles.Muted.Render(a.configLabel())),
	)

	dispRows := a.displayRows()
	items := allSettings()

	for i := 0; i < l.window; i++ {
		line, index := "", s.offset+i
		if index < len(dispRows) {
			row := dispRows[index]
			if row.isHeader {
				line = "  " + a.styles.SectionHdr.Render(row.section)
			} else {
				item := items[row.itemIdx]
				line = a.settingLine(item, row.itemIdx == s.cursor, l.inner)
			}
		}
		box = append(box, a.frameRow(line, l.inner))
	}

	if l.detail {
		box = append(box, a.frameRule(l.width))
		curDetail := ""
		if s.cursor < len(items) {
			curDetail = items[s.cursor].detail
		}
		lines := wrapLines(curDetail, l.inner, settingsDetailLines)
		for i := 0; i < settingsDetailLines; i++ {
			text := ""
			if i < len(lines) {
				text = lines[i]
			}
			box = append(box, a.frameRow(a.styles.Muted.Render(text), l.inner))
		}
	}

	box = append(box, a.frameRule(l.width))
	text, style := a.settingsNotice()
	lines := wrapLines(text, l.inner, l.noticeLines)
	for i := 0; i < l.noticeLines; i++ {
		line := ""
		if i < len(lines) {
			line = style.Render(lines[i])
		}
		box = append(box, a.frameRow(line, l.inner))
	}
	return a.closeBox(box, l, a.settingsHints())
}

// settingLine renders an individual setting item row.
func (a *App) settingLine(item settingDescriptor, selected bool, width int) string {
	prefix, titleStyle := "  ", a.styles.Text
	if selected {
		prefix, titleStyle = a.styles.SelectBar.Render("▌")+" ", a.styles.Title
	}

	box, state, stateStyle := "   ", "", a.styles.Accent
	if item.getDisplay != nil {
		state = item.getDisplay(a)
	}
	if item.kind == settingKindBool {
		if item.isEnabled != nil && item.isEnabled(a) {
			box, state, stateStyle = "[✓]", "on", a.styles.Success
		} else {
			box, state, stateStyle = "[ ]", "off", a.styles.Muted
		}
	} else if item.isDefault != nil && item.isDefault(a) {
		stateStyle = a.styles.Muted
	}

	if selected && a.settings.mode == settingsModeEdit {
		inputView := a.settings.input.View()
		room := max(width-2-lenOf(box)-1, 1)
		title := titleStyle.Render(truncatePlain(item.title, max(room-lenOf(inputView)-4, 1)))
		return prefix + box + " " + justify(room, title, a.styles.Title.Render(inputView))
	}

	room := max(width-2-lenOf(box)-1, 1)
	title := titleStyle.Render(truncatePlain(item.title, max(room-lenOf(state)-2, 1)))
	if item.kind == settingKindBool {
		return prefix + stateStyle.Render(box) + " " + justify(room, title, stateStyle.Render(state))
	}
	return prefix + box + " " + justify(room, title, stateStyle.Render(state))
}

// settingsNotice returns current status notice and style.
func (a *App) settingsNotice() (string, lipgloss.Style) {
	s := &a.settings
	switch {
	case s.notice != "" && s.noticeErr:
		return s.notice, a.styles.Error
	case s.notice != "":
		return s.notice, a.styles.Success
	case s.unavailable != "":
		return s.unavailable, a.styles.Error
	case a.notifyErr != nil && a.homeCfg.Notifications.Any():
		return "the last check for changes failed: " + a.notifyErr.Error(), a.styles.Error
	}
	return "Changes are saved as you make them. " +
		"prutil checks your open pull requests every " + humanInterval(a.notifyInterval()) +
		" while a notification is on.", a.styles.Muted
}

// humanInterval writes a poll interval the way a sentence would.
func humanInterval(d time.Duration) string {
	switch {
	case d == time.Minute:
		return "minute"
	case d > 0 && d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d > 0 && d < time.Minute && d%time.Second == 0:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	return model.HumanDuration(d)
}

// configLabel names the file the pane saves to.
func (a *App) configLabel() string {
	if a.store == nil {
		return "not saved"
	}
	path := a.store.Path(home.ConfigFile)
	if dir, err := os.UserHomeDir(); err == nil && dir != "" {
		if rel, err := filepath.Rel(dir, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join("~", rel)
		}
	}
	return path
}

// settingsHints names key combinations for the pane's bottom edge.
func (a *App) settingsHints() string {
	k := a.settings.keys
	pairs := []key.Help{
		{Key: k.Up.Help().Key + k.Down.Help().Key, Desc: "select"},
		k.Toggle.Help(),
		k.Test.Help(),
		k.Default.Help(),
		k.NextSec.Help(),
		k.Close.Help(),
	}
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, a.styles.OverlayKey.Render(p.Key)+" "+a.styles.Muted.Render(p.Desc))
	}
	return strings.Join(parts, a.styles.Muted.Render(" · "))
}

// settingsTestMsg reports how the test notification went.
type settingsTestMsg struct {
	err error
}

// capitalise upper-cases the first letter of a noun for a prompt label.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
