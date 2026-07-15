package tui

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"atenea/internal/session"
)

func TestNewResumePicker_OpensFocusedAndLoading(t *testing.T) {
	picker := newResumePicker("current-session")

	if !picker.open {
		t.Fatal("open = false, want true")
	}
	if !picker.loading {
		t.Fatal("loading = false, want true")
	}
	if picker.currentID != "current-session" {
		t.Fatalf("currentID = %q, want %q", picker.currentID, "current-session")
	}
	if !picker.query.Focused() {
		t.Fatal("query is not focused")
	}
	if picker.query.Prompt != "" {
		t.Fatalf("Prompt = %q, want empty", picker.query.Prompt)
	}
	if picker.query.Placeholder != "Search sessions" {
		t.Fatalf("Placeholder = %q, want %q", picker.query.Placeholder, "Search sessions")
	}
}

func TestResumePicker_SetSessionsCopiesAndResetsState(t *testing.T) {
	picker := newResumePicker("current")
	picker.setSessions([]session.SessionSummary{
		{ID: "one", Title: "Previous first"},
		{ID: "two", Title: "Previous second"},
	})
	sessions := []session.SessionSummary{
		{ID: "three", Title: "First session"},
		{ID: "one", Title: "Second session"},
	}
	picker.err = errors.New("load failed")
	picker.selected = 1

	picker.setSessions(sessions)

	if picker.loading {
		t.Fatal("loading = true, want false")
	}
	if picker.err != nil {
		t.Fatalf("err = %v, want nil", picker.err)
	}
	if picker.selected != 0 {
		t.Fatalf("selected = %d, want 0", picker.selected)
	}
	if got := sessionIDs(picker.filtered); !reflect.DeepEqual(got, []string{"three", "one"}) {
		t.Fatalf("filtered IDs = %v, want [three one]", got)
	}

	sessions[0].Title = "mutated"
	if picker.sessions[0].Title != "First session" {
		t.Fatalf("stored title = %q, input slice was aliased", picker.sessions[0].Title)
	}
}

func TestResumePicker_FilterUsesTrimmedCaseInsensitiveTitleSubstring(t *testing.T) {
	picker := newResumePicker("")
	picker.setSessions([]session.SessionSummary{
		{ID: "one", Title: "Planning Session"},
		{ID: "two", Title: "Bug triage"},
		{ID: "three", Title: "Release PLAN"},
	})
	picker.query.SetValue("  pLaN  ")

	picker.filter()

	if got := sessionIDs(picker.filtered); !reflect.DeepEqual(got, []string{"one", "three"}) {
		t.Fatalf("filtered IDs = %v, want [one three]", got)
	}
}

func TestResumePicker_FilterEmptyQueryMatchesAll(t *testing.T) {
	picker := newResumePicker("")
	sessions := []session.SessionSummary{
		{ID: "one", Title: "First"},
		{ID: "two", Title: "Second"},
	}
	picker.setSessions(sessions)
	picker.query.SetValue(" \t ")

	picker.filter()

	if got := sessionIDs(picker.filtered); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("filtered IDs = %v, want [one two]", got)
	}
}

func TestResumePicker_FilterClampsSelection(t *testing.T) {
	picker := newResumePicker("")
	picker.setSessions([]session.SessionSummary{
		{ID: "one", Title: "Alpha"},
		{ID: "two", Title: "Beta"},
		{ID: "three", Title: "Gamma"},
	})
	picker.selected = 2
	picker.query.SetValue("beta")

	picker.filter()

	if picker.selected != 0 {
		t.Fatalf("selected = %d, want 0", picker.selected)
	}
}

func TestResumePicker_FilterPreservesSelectedSessionIdentity(t *testing.T) {
	picker := newResumePicker("")
	picker.setSessions([]session.SessionSummary{
		{ID: "one", Title: "Alpha"},
		{ID: "two", Title: "Beta match"},
		{ID: "three", Title: "Gamma match"},
	})
	picker.selected = 2
	picker.query.SetValue("match")

	picker.filter()

	if picker.selected != 1 {
		t.Fatalf("selected = %d, want 1", picker.selected)
	}
	selected, ok := picker.selectedSession()
	if !ok || selected.ID != "three" {
		t.Fatalf("selectedSession = %+v, %v; want ID three, true", selected, ok)
	}
}

func TestResumePicker_FilterMatchesUnicodeEquivalents(t *testing.T) {
	tests := []struct {
		name  string
		title string
		query string
	}{
		{name: "composed query", title: "Cafe\u0301 planning", query: "CAFÉ"},
		{name: "decomposed query", title: "Café planning", query: "CAFE\u0301"},
		{name: "case fold expansion", title: "Straße notes", query: "STRASSE"},
		{name: "folded combining sequence", title: "ΐ", query: "Ϊ́"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			picker := newResumePicker("")
			picker.setSessions([]session.SessionSummary{{ID: "match", Title: tt.title}})
			picker.query.SetValue(tt.query)

			picker.filter()

			if got := sessionIDs(picker.filtered); !reflect.DeepEqual(got, []string{"match"}) {
				t.Fatalf("filtered IDs = %v, want [match]", got)
			}
		})
	}
}

func TestResumePicker_MoveWrapsCyclically(t *testing.T) {
	picker := newResumePicker("")
	picker.setSessions([]session.SessionSummary{
		{ID: "one", Title: "First"},
		{ID: "two", Title: "Second"},
		{ID: "three", Title: "Third"},
	})

	picker.move(-1)
	if picker.selected != 2 {
		t.Fatalf("selected after moving up = %d, want 2", picker.selected)
	}
	picker.move(1)
	if picker.selected != 0 {
		t.Fatalf("selected after moving down = %d, want 0", picker.selected)
	}
	picker.move(4)
	if picker.selected != 1 {
		t.Fatalf("selected after moving four = %d, want 1", picker.selected)
	}
}

func TestResumePicker_EmptyResultsHaveSafeSelection(t *testing.T) {
	picker := newResumePicker("")
	picker.setSessions([]session.SessionSummary{{ID: "one", Title: "First"}})
	picker.selected = 3
	picker.query.SetValue("missing")

	picker.filter()
	picker.move(1)
	selected, ok := picker.selectedSession()

	if len(picker.filtered) != 0 {
		t.Fatalf("filtered length = %d, want 0", len(picker.filtered))
	}
	if picker.selected != 0 {
		t.Fatalf("selected = %d, want 0", picker.selected)
	}
	if ok {
		t.Fatalf("selectedSession = %+v, true; want zero, false", selected)
	}
}

func TestResumePicker_SelectedSessionIsSafe(t *testing.T) {
	picker := newResumePicker("")
	picker.setSessions([]session.SessionSummary{{ID: "one", Title: "First"}})

	selected, ok := picker.selectedSession()
	if !ok || selected.ID != "one" {
		t.Fatalf("selectedSession = %+v, %v; want ID one, true", selected, ok)
	}

	picker.selected = 9
	if selected, ok := picker.selectedSession(); ok {
		t.Fatalf("selectedSession out of range = %+v, true; want zero, false", selected)
	}
}

func TestResumePicker_CloseResetsTransientStateAndBlursInput(t *testing.T) {
	picker := newResumePicker("current")
	picker.err = errors.New("failed")

	picker.close()

	if picker.open {
		t.Fatal("open = true, want false")
	}
	if picker.loading {
		t.Fatal("loading = true, want false")
	}
	if picker.err != nil {
		t.Fatalf("err = %v, want nil", picker.err)
	}
	if picker.query.Focused() {
		t.Fatal("query remains focused")
	}
}

func TestResumePicker_FailStopsLoadingAndStoresMessage(t *testing.T) {
	picker := newResumePicker("current")

	picker.fail("sessions unavailable")

	if picker.loading {
		t.Fatal("loading = true, want false")
	}
	if picker.err == nil || picker.err.Error() != "sessions unavailable" {
		t.Fatalf("err = %v, want %q", picker.err, "sessions unavailable")
	}
}

func TestFormatResumeActivity_UsesLocalTimeAndExactLayout(t *testing.T) {
	activity := time.Date(2026, time.July, 14, 16, 9, 45, 0, time.Local)

	if got := formatResumeActivity(activity); got != "Jul 14, 2026 16:09" {
		t.Fatalf("formatResumeActivity = %q, want %q", got, "Jul 14, 2026 16:09")
	}
}

func sessionIDs(sessions []session.SessionSummary) []string {
	ids := make([]string, len(sessions))
	for i, summary := range sessions {
		ids[i] = summary.ID
	}
	return ids
}
