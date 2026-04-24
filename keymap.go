package main

import "github.com/charmbracelet/bubbletea"

// isKeyUp returns true for up/k keys.
func isKeyUp(msg tea.KeyMsg) bool {
	return msg.String() == "up" || msg.String() == "k"
}

// isKeyDown returns true for down/j keys.
func isKeyDown(msg tea.KeyMsg) bool {
	return msg.String() == "down" || msg.String() == "j"
}

// isKeyRight returns true for right/l/enter keys.
func isKeyRight(msg tea.KeyMsg) bool {
	return msg.String() == "right" || msg.String() == "l" || msg.String() == "enter"
}

// isKeyLeft returns true for left/h/esc keys.
func isKeyLeft(msg tea.KeyMsg) bool {
	return msg.String() == "left" || msg.String() == "h" || msg.String() == "esc"
}

// isKeyQuit returns true for q/ctrl+c keys.
func isKeyQuit(msg tea.KeyMsg) bool {
	return msg.String() == "q" || msg.String() == "ctrl+c"
}
