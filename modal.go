package main

import (
	"fmt"
	"loglens/line"
	"loglens/stats"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// modalPage identifies the active wizard step. Each page is rendered as a
// centered overlay; navigation keys move within the page, "esc" backs up.
type modalPage int

const (
	modalPageAction modalPage = iota
	modalPageStatType
	modalPageConfig
)

// groupDraft is one user-editable grouping rule on the config page. The
// patterns are stored unanchored — stats.GroupRule.Compile adds anchors when
// the modal commits.
type groupDraft struct {
	titleInput   textinput.Model
	patternInput textinput.Model
}

// modalState holds the wizard's full state. nil on the model when no modal
// is open.
type modalState struct {
	page modalPage

	// Captured at open from the cursor in the JSON tree:
	fieldPath    []string // resolved JSON path; e.g. ["api_name"]
	fieldDisplay string   // leaf key, used as default stat name

	// Page-1 / page-2 selection cursor:
	cursor int

	// Page-3 (config) state:
	nameInput   textinput.Model
	sampleInput textinput.Model
	groups      []groupDraft

	// configFocus is the currently focused config item. The mapping between
	// focus index and item is computed by configFocusables() — keeps the
	// handler agnostic of how many groups are present.
	configFocus int
	statType    stats.StatType
}

// configItemKind classifies one focusable element on the config page.
type configItemKind int

const (
	itemName configItemKind = iota
	itemSample
	itemGroupTitle
	itemGroupPattern
	itemAddGroup
	itemSave
	itemCancel
)

// configItem is one entry in the focus order list.
type configItem struct {
	kind     configItemKind
	groupIdx int // valid when kind is itemGroupTitle / itemGroupPattern
}

// configFocusables returns the linear focus order across name, sample, every
// group's two text inputs, the add-group button, save, and cancel. Recomputed
// on each handler tick so we don't have to track stale indices when groups
// are added or removed.
func (ms *modalState) configFocusables() []configItem {
	items := []configItem{{kind: itemName}, {kind: itemSample}}
	for i := range ms.groups {
		items = append(items,
			configItem{kind: itemGroupTitle, groupIdx: i},
			configItem{kind: itemGroupPattern, groupIdx: i},
		)
	}
	items = append(items,
		configItem{kind: itemAddGroup},
		configItem{kind: itemSave},
		configItem{kind: itemCancel},
	)
	return items
}

// openFieldActionModal initializes a fresh modal anchored to the JSON field
// the cursor is currently on. Returns false if the cursor isn't on an
// extractable field path (cursor on parent line, non-JSON line, etc.).
func (m *model) openFieldActionModal() bool {
	if m.cursor < 0 || m.cursor >= m.store.Len() {
		return false
	}
	if len(m.cursorPath) == 0 {
		return false
	}
	root := m.store.Get(m.cursor)
	path, ok := resolveJSONFieldPath(root, m.cursorPath)
	if !ok || len(path) == 0 {
		return false
	}
	display := path[len(path)-1]
	m.modal = &modalState{
		page:         modalPageAction,
		fieldPath:    path,
		fieldDisplay: display,
	}
	return true
}

// resolveJSONFieldPath walks the cursor path through `root.Children` and
// extracts the JSON key at each step from the child's Raw (`"key": value`).
// Array indices are rejected — frequency stats only make sense on object
// keys, not positional array slots.
func resolveJSONFieldPath(root *line.LogLine, cursorPath []int) ([]string, bool) {
	cur := root
	var path []string
	for _, idx := range cursorPath {
		if cur.Children == nil || idx < 0 || idx >= len(cur.Children) {
			return nil, false
		}
		child := cur.Children[idx]
		key, ok := extractObjectKey(child.Raw)
		if !ok {
			return nil, false
		}
		path = append(path, key)
		cur = child
	}
	return path, true
}

// extractObjectKey parses a child LogLine's Raw of form `"key": <summary>` and
// returns the unquoted key. Returns false for array entries (`[N]: ...`) since
// those don't make sense as a frequency-tracking path.
func extractObjectKey(raw string) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	end := strings.Index(raw[1:], "\"")
	if end < 0 {
		return "", false
	}
	return raw[1 : 1+end], true
}

// initConfigPage allocates the text inputs for the config page using sensible
// defaults: stat name = leaf field, sample size = 20000, no groups.
func (ms *modalState) initConfigPage() {
	name := textinput.New()
	name.SetValue(ms.fieldDisplay)
	name.CharLimit = 64
	name.Width = 24
	ms.nameInput = name

	sample := textinput.New()
	sample.SetValue("20000")
	sample.CharLimit = 9
	sample.Width = 12
	ms.sampleInput = sample

	ms.groups = nil
	ms.configFocus = 0
	ms.refocusConfig()
}

// addGroup appends a fresh empty group rule below any existing ones and
// moves focus to its title field.
func (ms *modalState) addGroup() {
	title := textinput.New()
	title.CharLimit = 32
	title.Width = 14
	pat := textinput.New()
	pat.CharLimit = 64
	pat.Width = 22
	ms.groups = append(ms.groups, groupDraft{titleInput: title, patternInput: pat})

	// Focus the new group's title (focus index = 2 + 2*(N-1), since index 0
	// is name, 1 is sample, then 2 entries per group).
	ms.configFocus = 2 + 2*(len(ms.groups)-1)
	ms.refocusConfig()
}

// removeGroup drops group i and shifts focus to the previous focusable item.
func (ms *modalState) removeGroup(i int) {
	if i < 0 || i >= len(ms.groups) {
		return
	}
	ms.groups = append(ms.groups[:i], ms.groups[i+1:]...)
	if ms.configFocus > 1 {
		ms.configFocus--
	}
	if ms.configFocus < 0 {
		ms.configFocus = 0
	}
	ms.refocusConfig()
}

// refocusConfig sets Focus on exactly the active textinput (if the focused
// item is one) and Blurs all others. Bubbles' textinput uses the focus state
// to decide whether to render its caret and whether to consume key events.
func (ms *modalState) refocusConfig() {
	items := ms.configFocusables()
	if ms.configFocus < 0 {
		ms.configFocus = 0
	}
	if ms.configFocus >= len(items) {
		ms.configFocus = len(items) - 1
	}
	ms.nameInput.Blur()
	ms.sampleInput.Blur()
	for i := range ms.groups {
		ms.groups[i].titleInput.Blur()
		ms.groups[i].patternInput.Blur()
	}
	switch items[ms.configFocus].kind {
	case itemName:
		ms.nameInput.Focus()
	case itemSample:
		ms.sampleInput.Focus()
	case itemGroupTitle:
		ms.groups[items[ms.configFocus].groupIdx].titleInput.Focus()
	case itemGroupPattern:
		ms.groups[items[ms.configFocus].groupIdx].patternInput.Focus()
	}
}

// buildDefinition translates the user's modal input into a stats.Definition
// ready to hand to the manager. Returns false if the sample size doesn't
// parse — the modal handler shows a toast and keeps the modal open.
func (ms *modalState) buildDefinition() (stats.Definition, bool) {
	name := strings.TrimSpace(ms.nameInput.Value())
	if name == "" {
		name = ms.fieldDisplay
	}
	sample, err := strconv.Atoi(strings.TrimSpace(ms.sampleInput.Value()))
	if err != nil || sample < 0 {
		return stats.Definition{}, false
	}
	var rules []stats.GroupRule
	for _, g := range ms.groups {
		t := strings.TrimSpace(g.titleInput.Value())
		p := strings.TrimSpace(g.patternInput.Value())
		if t == "" || p == "" {
			continue // silently skip half-filled rows
		}
		rules = append(rules, stats.GroupRule{Title: t, Pattern: p})
	}
	return stats.Definition{
		Name:         name,
		FieldPath:    append([]string(nil), ms.fieldPath...),
		Type:         ms.statType,
		Groups:       rules,
		BackfillSize: sample,
	}, true
}

// updateModal dispatches a key event to the active modal page. Returns the
// updated model and any tea.Cmd produced by an embedded textinput (e.g. the
// blink command).
func (m model) updateModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.modal == nil {
		return m, nil
	}
	switch m.modal.page {
	case modalPageAction:
		return m.updateModalAction(msg)
	case modalPageStatType:
		return m.updateModalStatType(msg)
	case modalPageConfig:
		return m.updateModalConfig(msg)
	}
	return m, nil
}

func (m model) updateModalAction(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := []string{"Capture stats"}
	switch msg.String() {
	case "esc":
		m.modal = nil
		return m, nil
	case "up", "k":
		if m.modal.cursor > 0 {
			m.modal.cursor--
		}
	case "down", "j":
		if m.modal.cursor < len(items)-1 {
			m.modal.cursor++
		}
	case "enter":
		switch m.modal.cursor {
		case 0:
			m.modal.page = modalPageStatType
			m.modal.cursor = 0
		}
	}
	return m, nil
}

// statTypeOption is one row of the stat-type picker. enabled=false rows show
// a "not implemented" toast on selection rather than progressing the wizard.
type statTypeOption struct {
	label    string
	enabled  bool
	statType stats.StatType
}

func statTypeOptions() []statTypeOption {
	return []statTypeOption{
		{"Frequency", true, stats.Frequency},
		{"Average", false, stats.Average},
		{"P99", false, stats.P99},
		{"Min", false, stats.Min},
		{"Max", false, stats.Max},
	}
}

func (m model) updateModalStatType(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	opts := statTypeOptions()
	switch msg.String() {
	case "esc":
		m.modal.page = modalPageAction
		m.modal.cursor = 0
		return m, nil
	case "up", "k":
		if m.modal.cursor > 0 {
			m.modal.cursor--
		}
	case "down", "j":
		if m.modal.cursor < len(opts)-1 {
			m.modal.cursor++
		}
	case "enter":
		opt := opts[m.modal.cursor]
		if !opt.enabled {
			m.showToast(fmt.Sprintf("%s: not implemented", opt.label))
			return m, nil
		}
		m.modal.statType = opt.statType
		m.modal.page = modalPageConfig
		m.modal.initConfigPage()
		return m, textinput.Blink
	}
	return m, nil
}

func (m model) updateModalConfig(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := m.modal.configFocusables()
	if len(items) == 0 {
		return m, nil
	}
	cur := items[m.modal.configFocus]

	// Modal-level shortcuts that always win over textinput input.
	switch msg.String() {
	case "esc":
		m.modal = nil
		return m, nil
	case "tab":
		m.modal.configFocus = (m.modal.configFocus + 1) % len(items)
		m.modal.refocusConfig()
		return m, nil
	case "shift+tab":
		m.modal.configFocus--
		if m.modal.configFocus < 0 {
			m.modal.configFocus = len(items) - 1
		}
		m.modal.refocusConfig()
		return m, nil
	case "up":
		if m.modal.configFocus > 0 {
			m.modal.configFocus--
			m.modal.refocusConfig()
		}
		return m, nil
	case "down":
		if m.modal.configFocus < len(items)-1 {
			m.modal.configFocus++
			m.modal.refocusConfig()
		}
		return m, nil
	}

	// Per-item handling.
	switch cur.kind {
	case itemAddGroup:
		if msg.String() == "enter" {
			m.modal.addGroup()
			return m, textinput.Blink
		}
	case itemSave:
		if msg.String() == "enter" {
			return m.commitStat()
		}
	case itemCancel:
		if msg.String() == "enter" {
			m.modal = nil
			return m, nil
		}
	case itemGroupTitle, itemGroupPattern:
		// Ctrl+D removes the current group rule entirely.
		if msg.String() == "ctrl+d" {
			m.modal.removeGroup(cur.groupIdx)
			return m, nil
		}
		var cmd tea.Cmd
		if cur.kind == itemGroupTitle {
			m.modal.groups[cur.groupIdx].titleInput, cmd = m.modal.groups[cur.groupIdx].titleInput.Update(msg)
		} else {
			m.modal.groups[cur.groupIdx].patternInput, cmd = m.modal.groups[cur.groupIdx].patternInput.Update(msg)
		}
		return m, cmd
	case itemName:
		var cmd tea.Cmd
		m.modal.nameInput, cmd = m.modal.nameInput.Update(msg)
		return m, cmd
	case itemSample:
		var cmd tea.Cmd
		m.modal.sampleInput, cmd = m.modal.sampleInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// commitStat builds the stats.Definition from the modal's inputs, registers it
// with the manager, and kicks off the backfill goroutine. The bracketed lock
// hand-off guarantees no live line is double-counted: the stat is registered
// while the ingestor is locked out, so any post-cutoff line is observed only
// via the live path and any pre-cutoff line is observed only via backfill.
func (m model) commitStat() (tea.Model, tea.Cmd) {
	def, ok := m.modal.buildDefinition()
	if !ok {
		m.showToast("Sample size must be a non-negative integer")
		return m, nil
	}
	if m.statsMgr == nil {
		m.modal = nil
		return m, nil
	}

	m.s.mu.Lock()
	st, err := m.statsMgr.Add(def)
	if err != nil {
		m.s.mu.Unlock()
		m.showToast("Group pattern invalid: " + err.Error())
		return m, nil
	}
	cutoff := m.s.store.Len()
	m.s.mu.Unlock()

	if def.BackfillSize > 0 {
		startBackfill(m.s, st, cutoff, def.BackfillSize)
	}

	m.statsLayout = statsLayoutSplit
	m.statsBoxFocused = len(m.statsMgr.All()) - 1
	m.modal = nil
	m.showToast(fmt.Sprintf("Tracking %s on %s", def.Type, strings.Join(def.FieldPath, ".")))
	return m, nil
}

// modalBoxStyle is the lipgloss frame applied to every modal page.
func (m model) modalBoxStyle(width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("69")).
		Padding(1, 2).
		Width(width)
}

// renderModal returns the centered overlay for the active modal, or "" when
// there's no modal. Caller is responsible for placing it inside the View
// layout (we use lipgloss.Place over the rendered viewport).
func (m model) renderModal() string {
	if m.modal == nil {
		return ""
	}
	switch m.modal.page {
	case modalPageAction:
		return m.renderModalAction()
	case modalPageStatType:
		return m.renderModalStatType()
	case modalPageConfig:
		return m.renderModalConfig()
	}
	return ""
}

func (m model) renderModalAction() string {
	width := minInt(60, m.width-4)
	if width < 30 {
		width = 30
	}
	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Bold(true).Render(
		fmt.Sprintf("Field: %s", strings.Join(m.modal.fieldPath, "."))))
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Faint(true).Render("What do you want to do?"))
	sb.WriteString("\n\n")
	items := []string{"Capture stats"}
	for i, it := range items {
		prefix := "  "
		style := lipgloss.NewStyle()
		if i == m.modal.cursor {
			prefix = "▸ "
			style = style.Bold(true).Foreground(lipgloss.Color("220"))
		}
		sb.WriteString(prefix)
		sb.WriteString(style.Render(it))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Faint(true).Render("enter: select   esc: close"))
	return m.modalBoxStyle(width).Render(sb.String())
}

func (m model) renderModalStatType() string {
	width := minInt(60, m.width-4)
	if width < 30 {
		width = 30
	}
	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Bold(true).Render(
		fmt.Sprintf("Field: %s", strings.Join(m.modal.fieldPath, "."))))
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Faint(true).Render("Pick a statistic to compute:"))
	sb.WriteString("\n\n")
	for i, opt := range statTypeOptions() {
		prefix := "  "
		nameStyle := lipgloss.NewStyle()
		if i == m.modal.cursor {
			prefix = "▸ "
			nameStyle = nameStyle.Bold(true).Foreground(lipgloss.Color("220"))
		}
		if !opt.enabled {
			nameStyle = nameStyle.Faint(true)
		}
		sb.WriteString(prefix)
		sb.WriteString(nameStyle.Render(opt.label))
		if !opt.enabled {
			sb.WriteString(lipgloss.NewStyle().Faint(true).Render("  (todo)"))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Faint(true).Render("enter: select   esc: back"))
	return m.modalBoxStyle(width).Render(sb.String())
}

func (m model) renderModalConfig() string {
	width := minInt(76, m.width-4)
	if width < 40 {
		width = 40
	}
	var sb strings.Builder
	sb.WriteString(lipgloss.NewStyle().Bold(true).Render(
		fmt.Sprintf("Configure %s on %s", m.modal.statType,
			strings.Join(m.modal.fieldPath, "."))))
	sb.WriteString("\n\n")

	items := m.modal.configFocusables()
	for i, it := range items {
		focused := i == m.modal.configFocus
		switch it.kind {
		case itemName:
			sb.WriteString(renderField(focused, "Name", m.modal.nameInput.View()))
		case itemSample:
			sb.WriteString(renderField(focused, "Backfill", m.modal.sampleInput.View()+" lines"))
		case itemGroupTitle:
			if it.groupIdx == 0 {
				sb.WriteString(lipgloss.NewStyle().Faint(true).Render(
					"Grouping (regex per row, first match wins; unmatched values form their own group):"))
				sb.WriteString("\n")
			}
			label := fmt.Sprintf("  Group %d title", it.groupIdx+1)
			sb.WriteString(renderField(focused, label, m.modal.groups[it.groupIdx].titleInput.View()))
		case itemGroupPattern:
			label := fmt.Sprintf("  Group %d pattern", it.groupIdx+1)
			sb.WriteString(renderField(focused, label, m.modal.groups[it.groupIdx].patternInput.View()))
		case itemAddGroup:
			sb.WriteString(renderButton(focused, "+ Add group"))
		case itemSave:
			sb.WriteString(renderButton(focused, "Save and start"))
		case itemCancel:
			sb.WriteString(renderButton(focused, "Cancel"))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Faint(true).Render(
		"tab/↑↓: focus  ctrl+d: delete group  enter: activate  esc: close"))
	return m.modalBoxStyle(width).Render(sb.String())
}

// renderField formats one labelled input row for the config modal.
func renderField(focused bool, label, value string) string {
	pointer := "  "
	labelStyle := lipgloss.NewStyle().Faint(true)
	if focused {
		pointer = "▸ "
		labelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	}
	return pointer + labelStyle.Render(label) + ": " + value
}

// renderButton formats a button-style row (no input, just a label).
func renderButton(focused bool, label string) string {
	if focused {
		return lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("220")).
			Render("▸ [ " + label + " ]")
	}
	return lipgloss.NewStyle().Faint(true).Render("  [ " + label + " ]")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
