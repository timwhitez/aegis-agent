package tui

import "github.com/charmbracelet/lipgloss"

var (
	appStyle = lipgloss.NewStyle().
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230"))

	subtleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	headerStateBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("238")).
				Padding(0, 1)

	headerMetaBadgeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Background(lipgloss.Color("236")).
				Padding(0, 1)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203")).
			Bold(true)

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("78"))

	warningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("221"))

	switchBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("166")).
				Padding(0, 1)

	runningBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("31")).
				Padding(0, 1)

	queueBadgeStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("24")).
			Padding(0, 1)

	pendingBadgeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("58")).
				Padding(0, 1)

	overlaySelectedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("24"))

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)

	focusedBorderStyle = borderStyle.Copy().
				BorderForeground(lipgloss.Color("39"))

	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")).
			Bold(true)

	tabActiveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("33")).
			Padding(0, 1)

	tabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245")).
				Padding(0, 1)
)
