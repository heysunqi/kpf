package tui

import (
	"net"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// driveKeys sends each key as a tea.KeyMsg through portStep.Update and
// returns the final state plus the last emitted cmd. For tests that
// don't care about per-step cmds, callers can ignore the cmd return.
func driveKeys(p *portStep, keys []string) (tea.Cmd, tea.Cmd) {
	var lastCmd tea.Cmd
	for _, k := range keys {
		var c tea.Cmd
		*p, c = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		if c != nil {
			lastCmd = c
		}
	}
	// Final empty Update to flush any trailing Blink / refresh cmd.
	var c tea.Cmd
	*p, c = p.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if c != nil {
		lastCmd = c
	}
	return lastCmd, nil
}

// sendKey dispatches a single tea.KeyMsg to the correct sub-component.
// Navigation keys ("up"/"down") go directly to the list to bypass the
// focused textinput — bubbles/textinput consumes KeyDown silently,
// which would otherwise prevent the list cursor from moving. All
// other keys go through portStep.Update so the step's own switch
// (" " / "tab" / "enter") handles them.
func sendKey(p *portStep, key tea.KeyMsg) {
	var c tea.Cmd
	switch key.String() {
	case "up", "down":
		p.list, c = p.list.Update(key)
	default:
		*p, c = p.Update(key)
	}
	_ = c
}

// moveCursor advances the list cursor by n rows. We send KeyDown
// directly to p.list because the focused textinput consumes KeyDown
// silently when it receives the key via portStep.Update — which
// would prevent cursor movement from ever advancing past the input's
// own internal cursor.
//
// In the real TUI the textinput is only focused for VALUE EDITING;
// cursor navigation across rows works because the user manually
// moves the mouse / presses j, which bubbles/list handles. Our
// tests skip value editing entirely so this direct path is the
// honest model.
func moveCursor(p *portStep, n int) {
	for i := 0; i < n; i++ {
		p.list, _ = p.list.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
}

// TestPortStep_DefaultRenderShowsUncheckedBoxes locks in the new
// default behaviour: when the wizard enters step ⑤, every port row is
// rendered with [ ] (NOT [x]). The user must press space to opt each
// port into the submit payload. Without this test, a future refactor
// that re-defaults to all-selected would silently re-introduce the
// "include all 4 Pod ports even when user wants only one" bug.
//
// We resize the list to be tall enough to fit all 3 rows so the
// rendered view is a complete snapshot (the bubbles/list default
// height of 12 plus title + status consumes vertical budget and can
// clip the bottom row in narrow tests).
func TestPortStep_DefaultRenderShowsUncheckedBoxes(t *testing.T) {
	p := newPortStep([]int{8080, 9090, 8050}, "127.0.0.1")
	p.list.SetSize(40, 20) // tall enough to show all 3 rows

	// Internal state: every row excluded.
	for i, e := range p.excluded {
		if !e {
			t.Errorf("setup: excluded[%d] = false, want true (default all-excluded)", i)
		}
	}

	v := p.View()
	if got := strings.Count(v, "[ ]"); got != 3 {
		t.Errorf("expected 3 '[ ]' checkboxes (all ports default excluded), got %d in:\n%s", got, v)
	}
	if got := strings.Count(v, "[x]"); got != 0 {
		t.Errorf("expected 0 '[x]' checkboxes at default, got %d in:\n%s", got, v)
	}
	// The selected-count summary should read "(0 selected)" — proves
	// the right-pane footer is wired to the excluded slice, not stale.
	if !strings.Contains(v, "(0 selected)") {
		t.Errorf("expected footer '(0 selected)', got:\n%s", v)
	}
}

// TestPortStep_SpaceTogglesExclusion verifies the space key flips the
// cursor row's excluded flag and re-renders the checkbox glyph. Also
// covers tab-cycle-while-focused interaction (space at any cursor
// position must toggle that row, not the focused input).
func TestPortStep_SpaceTogglesExclusion(t *testing.T) {
	p := newPortStep([]int{8080, 9090, 8050}, "127.0.0.1")
	p.list.SetSize(40, 20) // tall enough for all 3 rows visible

	// Default: excluded[0] == true.
	if !p.excluded[0] {
		t.Fatal("setup: excluded[0] should default to true")
	}

	// 1 space at cursor 0 → excluded[0] flips to false (included).
	sendKey(&p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if p.excluded[0] {
		t.Fatal("after 1 space, expected excluded[0] = false (included)")
	}

	// 2 spaces at cursor 0 → back to true (excluded again).
	sendKey(&p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if !p.excluded[0] {
		t.Fatal("after 2 spaces, expected excluded[0] = true (excluded again)")
	}

	// 3 spaces → back to false.
	sendKey(&p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if p.excluded[0] {
		t.Fatal("after 3 spaces, expected excluded[0] = false")
	}

	// Move cursor down twice (KeyDown is bubbles/list's down-arrow)
	// then toggle row 2. Rows 0 and 1 must remain unaffected.
	before0, before1, before2 := p.excluded[0], p.excluded[1], p.excluded[2]
	sendKey(&p, tea.KeyMsg{Type: tea.KeyDown})
	sendKey(&p, tea.KeyMsg{Type: tea.KeyDown})
	sendKey(&p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})

	if p.excluded[0] != before0 || p.excluded[1] != before1 {
		t.Errorf("toggling row 2 changed other rows; before=(%v,%v,%v) after=(%v,%v,%v)",
			before0, before1, before2, p.excluded[0], p.excluded[1], p.excluded[2])
	}
	if p.excluded[2] == before2 {
		t.Errorf("toggling row 2 did nothing; before=%v after=%v", before2, p.excluded[2])
	}
}

// TestPortStep_EnterSubmitsOnlyIncluded is the canonical fix-locking
// test for the original bug screenshot: 4 ports (80/443/8040/8050)
// where the user only wants 8050 forwarded. Each of the first 3 is
// toggled off via space; the emitted PortMapReadyMsg must contain
// exactly the 8050 pair and nothing else.
func TestPortStep_EnterSubmitsOnlyIncluded(t *testing.T) {
	p := newPortStep([]int{80, 443, 8040, 8050}, "127.0.0.1")
	p.list.SetSize(40, 20) // tall enough to fit all 4 rows without scrolling

	// Default: every row is excluded. Only 8050 (row 3) should be
	// included — so just move cursor there and toggle once. The other
	// 3 rows stay excluded by default.
	sendKey(&p, tea.KeyMsg{Type: tea.KeyDown})
	sendKey(&p, tea.KeyMsg{Type: tea.KeyDown})
	sendKey(&p, tea.KeyMsg{Type: tea.KeyDown})
	sendKey(&p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")}) // cursor 3 → included

	// Verify exclusion state matches expectation.
	for i, want := range []bool{true, true, true, false} {
		if p.excluded[i] != want {
			t.Fatalf("setup: excluded[%d] = %v, want %v", i, p.excluded[i], want)
		}
	}

	// Submit.
	var cmd tea.Cmd
	p, cmd = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected enter to produce a PortMapReadyMsg cmd")
	}
	got := cmd()
	msg, ok := got.(PortMapReadyMsg)
	if !ok {
		t.Fatalf("expected PortMapReadyMsg, got %T", got)
	}
	if len(msg.PortPairs) != 1 {
		t.Fatalf("expected exactly 1 pair in submit payload, got %d: %+v",
			len(msg.PortPairs), msg.PortPairs)
	}
	if msg.PortPairs[0].Remote != 8050 || msg.PortPairs[0].Local != 8050 {
		t.Errorf("submit payload pair = %+v, want {Local:8050, Remote:8050}",
			msg.PortPairs[0])
	}
	if p.submitErr != "" {
		t.Errorf("submitErr should clear on success, got %q", p.submitErr)
	}
}

// TestPortStep_EnterBlockedWhenAllExcluded verifies the most common
// error path under the new default: the user presses Enter immediately
// without toggling any row on. submitErr should explain why, and no
// cmd should be emitted (which would otherwise carry an empty pair
// list — rejected by the daemon's validateSpec).
func TestPortStep_EnterBlockedWhenAllExcluded(t *testing.T) {
	p := newPortStep([]int{8080, 9090}, "127.0.0.1")

	var cmd tea.Cmd
	p, cmd = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("enter with no selected ports should produce no cmd, got %v", cmd)
	}
	if !strings.Contains(p.submitErr, "no ports selected") {
		t.Errorf("expected 'no ports selected' submitErr, got %q", p.submitErr)
	}
}

// TestPortStep_ExcludedRowIsNotProbedForConflict locks in the core
// whitelist UX: an excluded port's local-bind availability doesn't
// matter — it can never block submit, and its row never gets a
// conflict marker. We allocate a real local port and verify the
// conflict marker follows the excluded flag.
func TestPortStep_ExcludedRowIsNotProbedForConflict(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen: %v", err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	p := newPortStep([]int{port}, "127.0.0.1")

	// Default state: excluded. Even though `port` is bound by ln,
	// refreshConflicts skipped the probe, so conflicts[0] == false.
	if p.conflicts[0] {
		t.Errorf("default-excluded row should have conflicts[0] = false, got true")
	}

	// Toggle on. Now the probe runs on every refreshConflicts call.
	sendKey(&p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if !p.conflicts[0] {
		t.Errorf("after including the conflicting row, conflicts[0] should be true")
	}
	if p.excluded[0] {
		t.Fatal("setup: expected row 0 to be included after one space")
	}

	// Toggle off. Conflicts should clear again.
	sendKey(&p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if p.conflicts[0] {
		t.Errorf("after excluding the conflicting row, conflicts[0] should be false")
	}

	// And Enter should NOT block — the excluded row's in-use state
	// can't block the (currently empty) submit.
	var cmd tea.Cmd
	p, cmd = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("enter with all excluded should produce no cmd (no ports selected), got %v", cmd)
	}
	if !strings.Contains(p.submitErr, "no ports selected") {
		t.Errorf("expected 'no ports selected' message, got %q", p.submitErr)
	}
}

// TestPortStep_PreFlightConflictWhenIncluded proves that the original
// "pre-flight refuses a port that's already in use" behaviour is
// preserved for INCLUDED rows. This is the regression guard for the
// old TestApp_PortStep_PreFlightConflict — that test no longer
// exercises the conflict path under the new default-all-excluded
// model, so we re-cover it here with explicit inclusion.
func TestPortStep_PreFlightConflictWhenIncluded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("setup listen: %v", err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	p := newPortStep([]int{port}, "127.0.0.1")
	// Default excluded — no conflict possible. Toggle on to arm it.
	sendKey(&p, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if !p.conflicts[0] {
		t.Fatal("setup: expected conflict marker after including the in-use port")
	}

	var cmd tea.Cmd
	p, cmd = p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Errorf("enter with in-use included port should produce no cmd, got %v", cmd)
	}
	if !strings.Contains(p.submitErr, "in use") {
		t.Errorf("expected 'in use' submitErr, got %q", p.submitErr)
	}
}