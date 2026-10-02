package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPastingIntoRepositorySettingsSubPanes(t *testing.T) {
	tests := []struct {
		name    string
		setting string
		paste   string
		value   func(*App) string
	}{
		{
			name:    "discovery root is pasted into its single input",
			setting: "discovery.roots",
			paste:   "~/.filetree/worktrees",
			value: func(a *App) string {
				if len(a.homeCfg.Discovery.Roots) == 0 {
					return ""
				}
				return a.homeCfg.Discovery.Roots[0]
			},
		},
		{
			name:    "repository name and path are pasted into their respective inputs",
			setting: "repos",
			paste:   "/work/widgets",
			value: func(a *App) string {
				return a.homeCfg.Repos["acme/widgets"]
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := settingsApp(t)
			openSettingsPane(t, app)
			cursorOn(t, app, tt.setting)
			send(t, app, press("enter"))
			send(t, app, press("a"))

			if tt.setting == "repos" {
				send(t, app, tea.PasteMsg{Content: "acme/widgets"})
				assert.Equal(t, "acme/widgets", app.settings.subPane.keyInput.Value())
				send(t, app, press("tab"))
			}
			send(t, app, tea.PasteMsg{Content: tt.paste})
			input := app.settings.subPane.keyInput.Value()
			if tt.setting == "repos" {
				input = app.settings.subPane.valInput.Value()
			}
			assert.Equal(t, tt.paste, input)
			send(t, app, press("enter"))

			require.Equal(t, tt.paste, tt.value(app))
			saved, err := app.store.LoadConfig()
			require.NoError(t, err)
			assert.Equal(t, tt.paste, tt.value(&App{homeCfg: saved}))
		})
	}
}
