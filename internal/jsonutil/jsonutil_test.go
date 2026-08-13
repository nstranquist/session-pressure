package jsonutil

import "testing"

func TestMarshal_sortsKeys(t *testing.T) {
	got := string(MustMarshal(map[string]any{
		"z": 1,
		"a": 2,
		"m": 3,
	}))
	want := `{"a":2,"m":3,"z":1}`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMarshal_nested(t *testing.T) {
	got := string(MustMarshal(map[string]any{
		"outer": map[string]any{
			"b": true,
			"a": []any{1, 2, 3},
		},
	}))
	want := `{"outer":{"a":[1,2,3],"b":true}}`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMarshal_null(t *testing.T) {
	if got := string(MustMarshal(nil)); got != "null" {
		t.Fatalf("got %q want null", got)
	}
}

func TestMarshal_integerFloat(t *testing.T) {
	if got := string(MustMarshal(42.0)); got != "42" {
		t.Fatalf("got %q want 42", got)
	}
}

func TestMarshal_stringNoHTMLEscape(t *testing.T) {
	got := string(MustMarshal("a<b>c&d"))
	want := `"a<b>c&d"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMarshal_emptyMapAndSlice(t *testing.T) {
	if got := string(MustMarshal(map[string]any{})); got != "{}" {
		t.Fatalf("got %q want {}", got)
	}
	if got := string(MustMarshal([]any{})); got != "[]" {
		t.Fatalf("got %q want []", got)
	}
}

func TestMarshal_deterministic(t *testing.T) {
	m := map[string]any{"b": 2, "a": 1}
	first := string(MustMarshal(m))
	for i := 0; i < 10; i++ {
		if got := string(MustMarshal(m)); got != first {
			t.Fatalf("non-deterministic: run %d got %q want %q", i, got, first)
		}
	}
}

func TestMarshal_structHonorsIgnoredAndUnnamedTags(t *testing.T) {
	type fixture struct {
		Visible string `json:"visible"`
		Secret  string `json:"-"`
		Count   int    `json:",omitempty"`
	}
	got := string(MustMarshal(fixture{Visible: "ok", Secret: "must-not-leak", Count: 2}))
	want := `{"Count":2,"visible":"ok"}`
	if got != want {
		t.Fatalf("Marshal() = %s, want %s", got, want)
	}
}

func TestCanonicalObject_sortsKeysAndPreservesNumbers(t *testing.T) {
	got, err := CanonicalObject([]byte(`{"z":2.0,"a":{"b":1,"a":[]}}`))
	if err != nil {
		t.Fatalf("CanonicalObject error: %v", err)
	}
	want := `{"a":{"a":[],"b":1},"z":2.0}`
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestCanonicalObject_requiresTopLevelObject(t *testing.T) {
	if _, err := CanonicalObject([]byte(`[]`)); err == nil {
		t.Fatal("CanonicalObject accepted top-level array")
	}
}

func TestCanonicalObject_rejectsTrailingJSON(t *testing.T) {
	if _, err := CanonicalObject([]byte(`{} {}`)); err == nil {
		t.Fatal("CanonicalObject accepted trailing JSON value")
	}
}
