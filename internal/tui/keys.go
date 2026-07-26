package tui

import "github.com/charmbracelet/bubbles/key"

// GlobalKeys holds keybindings shared across all steps.
type GlobalKeys struct {
	Quit   key.Binding
	Back   key.Binding
	Select key.Binding
	Active key.Binding
}

func DefaultGlobalKeys() GlobalKeys {
	return GlobalKeys{
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
		Back: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "back"),
		),
		Select: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "next"),
		),
		Active: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "active forwards"),
		),
	}
}

// ShortHelp returns key + description pairs for the footer.
func (g GlobalKeys) ShortHelp() []key.Binding {
	return []key.Binding{g.Select, g.Back, g.Active, g.Quit}
}
