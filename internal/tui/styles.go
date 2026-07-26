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

	// Status
	StatusOK = lipgloss.NewStyle().
			Foreground(success).
			Bold(true)

	StatusErr = lipgloss.NewStyle().
			Foreground(danger).
			Bold(true)

	StatusWarn = lipgloss.NewStyle().
			Foreground(warning)

	// Active view table
	TableHeader = lipgloss.NewStyle().
			Bold(true).
			Underline(true).
			Padding(0, 1)

	TableCell = lipgloss.NewStyle().
			Padding(0, 1)
)
