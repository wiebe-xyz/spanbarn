package main

import "github.com/charmbracelet/lipgloss"

var (
	primaryColor = lipgloss.Color("39")
	successColor = lipgloss.Color("82")
	errorColor   = lipgloss.Color("196")
	accentColor  = lipgloss.Color("86")

	borderColor = lipgloss.Color("240")
	selectedBg  = lipgloss.Color("237")
	titleColor  = lipgloss.Color("230")
	subtleColor = lipgloss.Color("243")
	dimColor    = lipgloss.Color("241")
	footerBg    = lipgloss.Color("235")

	headerStyle = lipgloss.NewStyle().
			Foreground(titleColor).
			Background(primaryColor).
			Padding(0, 2).
			Bold(true)

	footerStyle = lipgloss.NewStyle().
			Foreground(subtleColor).
			Background(footerBg).
			Padding(0, 2)

	selectedStyle = lipgloss.NewStyle().
			Foreground(titleColor).
			Background(selectedBg).
			Bold(true).
			PaddingLeft(1).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(accentColor)

	normalStyle = lipgloss.NewStyle().PaddingLeft(2)

	statusOK    = lipgloss.NewStyle().Foreground(successColor).Bold(true)
	statusError = lipgloss.NewStyle().Foreground(errorColor).Bold(true)

	serviceStyle = lipgloss.NewStyle().Foreground(accentColor)
	timeStyle    = lipgloss.NewStyle().Foreground(subtleColor)
	countStyle   = lipgloss.NewStyle().Foreground(dimColor)

	helpKeyStyle = lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	helpStyle    = lipgloss.NewStyle().Foreground(dimColor)
	helpSep      = lipgloss.NewStyle().Foreground(borderColor)
)

func statusStyle(status string) lipgloss.Style {
	if status == "error" {
		return statusError
	}
	return statusOK
}

func statusIcon(status string) string {
	if status == "error" {
		return "✗"
	}
	return "●"
}

func helpItem(key, desc string) string {
	return helpKeyStyle.Render(key) + helpSep.Render(" ") + helpStyle.Render(desc)
}
