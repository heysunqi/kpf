package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	accent  = lipgloss.Color("#7D56F4")
	muted   = lipgloss.Color("#626262")
	danger  = lipgloss.Color("#FF5F5F")
	success = lipgloss.Color("#04B575")
	warning = lipgloss.Color("#F2C14E")
	white   = lipgloss.Color("#FFFFFF")

	// Top-level styles
	HeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(white).
			Background(accent).
			Padding(0, 1)

	Breadcrumb = lipgloss.NewStyle().
			Foreground(muted).
			Padding(0, 1)

	BreadcrumbActive = lipgloss.NewStyle().
				Foreground(white).
				Bold(true)

	BreadcrumbSep = lipgloss.NewStyle().
			Foreground(muted)

	Body = lipgloss.NewStyle().
		Padding(1, 2)

	Footer = lipgloss.NewStyle().
		Foreground(muted).
		Padding(0, 1)

	FooterKey = lipgloss.NewStyle().
			Foreground(white).
			Bold(true)

	FooterSep = lipgloss.NewStyle().
			Foreground(muted)

	// List item styles
	ListTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(white).
			Padding(0, 0, 1, 0)

	ListHelp = lipgloss.NewStyle().
			Foreground(muted)

	ListItem = lipgloss.NewStyle().
			Padding(0, 2)

	ListItemSelected = lipgloss.NewStyle().
				Foreground(accent).
				Bold(true).
				Padding(0, 2)

	// Form layout
	FormLabel = lipgloss.NewStyle().
			Foreground(muted).
			Padding(0, 1)

	PortRow = lipgloss.NewStyle().
		Padding(0, 2)

	PortRowSelected = lipgloss.NewStyle().
			Foreground(accent).
			Bold(true).
			Padding(0, 2)

	// MutedText is a bare style with the muted foreground — used to
	// de-emphasize excluded port rows in the right pane (they're not
	// in the submit payload, so they shouldn't visually compete with
	// the included rows).
	MutedText = lipgloss.NewStyle().
			Foreground(muted)

	// Status
	StatusOK = lipgloss.NewStyle().
			Foreground(success).
			Bold(true)

	StatusErr = lipgloss.NewStyle().
			Foreground(danger).
			Bold(true)

	StatusWarn = lipgloss.NewStyle().
			Foreground(warning)

	// Active view table — these styles are applied by renderActiveHeader /
	// renderActiveRow in view_active.go. The header is plain bold text
	// (no border, no background) so the separator rule below it provides
	// the visual demarcation; the cursor row gets a colored background
	// to make the selection unmistakable across the long row.
	activeHeaderStyle = lipgloss.NewStyle().Bold(true)
	activeSelectedRowStyle = lipgloss.NewStyle().
				Foreground(white).
				Background(accent).
				Bold(true)
)
