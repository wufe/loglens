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

// isKeyRight returns true for right/l keys. Enter is handled separately so
// it can open the field action modal when the cursor sits on a JSON child.
func isKeyRight(msg tea.KeyMsg) bool {
	return msg.String() == "right" || msg.String() == "l"
}

// isKeyEnter returns true for the enter key.
func isKeyEnter(msg tea.KeyMsg) bool {
	return msg.String() == "enter"
}

// isKeyTab returns true for the tab key — toggles focus between log viewport
// and stats container.
func isKeyTab(msg tea.KeyMsg) bool {
	return msg.String() == "tab"
}

// isKeyZoom returns true for the "z" key — toggles full-height for the
// currently focused pane (logs or stats).
func isKeyZoom(msg tea.KeyMsg) bool {
	return msg.String() == "z"
}

// isKeyLeft returns true for left/h/esc keys.
func isKeyLeft(msg tea.KeyMsg) bool {
	return msg.String() == "left" || msg.String() == "h" || msg.String() == "esc"
}

// isKeyQuit returns true for q/ctrl+c keys.
func isKeyQuit(msg tea.KeyMsg) bool {
	return msg.String() == "q" || msg.String() == "ctrl+c"
}
