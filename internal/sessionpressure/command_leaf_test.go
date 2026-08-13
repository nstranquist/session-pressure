package sessionpressure

import "testing"

func TestCommandLeafVocabulary(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, "snapshot"},
		{[]string{"board"}, "board"},
		{[]string{"work", "stats"}, "work.stats"},
		{[]string{"work", "run"}, "work.run"},
		{[]string{"cleanup", "claim", "heartbeat"}, "cleanup.claim.heartbeat"},
		{[]string{"policy", "profile", "show"}, "policy.profile.show"},
		{[]string{"not-real"}, "unknown"},
	}
	for _, tc := range cases {
		if got := CommandLeaf(tc.args); got != tc.want {
			t.Fatalf("CommandLeaf(%v)=%q want %q", tc.args, got, tc.want)
		}
	}
}

func TestCommandLeafFromCLIArgsStopsAtDelimiter(t *testing.T) {
	got := CommandLeafFromCLIArgs([]string{
		"--json", "session", "pressure", "work", "run", "--class", "test", "--", "ndev", "session", "pressure", "status",
	})
	if got != "work.run" {
		t.Fatalf("got %q", got)
	}
	if CommandLeafFromCLIArgs([]string{"catalog", "search"}) != "" {
		t.Fatal("non-pressure path should be empty")
	}
}
