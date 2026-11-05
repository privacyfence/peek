package cli

import (
	"strings"
	"testing"
)

func TestCovStatsAndCommentsUsageErrors(t *testing.T) {
	configureTestClient(t, "http://127.0.0.1:1")
	cases := map[string]func() error{
		"stats arity":          func() error { return cmdStats(nil) },
		"stats slug":           func() error { return cmdStats([]string{"bad/slug"}) },
		"comments no value":    func() error { return cmdComments([]string{"demo", "--version"}) },
		"comments bad version": func() error { return cmdComments([]string{"demo", "--version", "0"}) },
		"comments dup slug":    func() error { return cmdComments([]string{"demo", "other"}) },
		"comments flag":        func() error { return cmdComments([]string{"demo", "--nope"}) },
		"comments no slug":     func() error { return cmdComments(nil) },
		"comments bad slug":    func() error { return cmdComments([]string{"bad/slug"}) },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			if err := fn(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestCovStatsAndCommentsEnvFailures(t *testing.T) {
	t.Run("stats", func(t *testing.T) {
		covEnvFailures(t, func() error { return cmdStats([]string{"demo"}) })
	})
	t.Run("comments", func(t *testing.T) {
		covEnvFailures(t, func() error { return cmdComments([]string{"demo", "--version", "2"}) })
	})
}

func TestCovStatsPrintsRecentVisits(t *testing.T) {
	ts := covFake(t, map[string]string{"/api/uploads/demo/stats": `{"slug":"demo","name":"Demo","total_visits":3,"unique_visitors":2,"recent":[{"name":"","ip":"abc","user_agent":"ua","visited_at":0},{"name":"Ann","ip":"def","user_agent":"ua","visited_at":0}]}`}, nil)
	configureTestClient(t, ts.URL)
	out, err := captureStdout(t, func() error { return cmdStats([]string{"demo"}) })
	if err != nil || !strings.Contains(out, "(anonymous)") || !strings.Contains(out, "Ann") {
		t.Fatalf("got %q %v", out, err)
	}
}

func TestCovCommentsKindAndContextVariants(t *testing.T) {
	ts := covFake(t, map[string]string{"/api/uploads/demo/comments": `[
		{"id":1,"author":"","body":"page","version":1,"created_at":0},
		{"id":2,"author":"Ann","selector":"p.x","element_text":"hello","body":"text","version":1,"created_at":0},
		{"id":3,"author":"Bob","selector":"div.y","body":"element","version":2,"created_at":0},
		{"id":4,"author":"Cy","anchor_kind":"text","body":"empty text","version":2,"created_at":0}
	]`, "/api/uploads/empty/comments": `[]`}, nil)
	configureTestClient(t, ts.URL)
	out, err := captureStdout(t, func() error { return cmdComments([]string{"demo"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"anonymous", "“hello”", "on: div.y", "on: whole page"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	out, err = captureStdout(t, func() error { return cmdComments([]string{"empty"}) })
	if err != nil || !strings.Contains(out, "no comments yet") {
		t.Fatalf("empty: %q %v", out, err)
	}
}

func statsOutput(t *testing.T, body string) string {
	t.Helper()
	ts := covFake(t, map[string]string{"/api/uploads/demo/stats": body}, nil)
	configureTestClient(t, ts.URL)
	out, err := captureStdout(t, func() error { return cmdStats([]string{"demo"}) })
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStatsTokenCalls(t *testing.T) {
	out := statsOutput(t, `{"slug":"demo","name":"Demo","total_visits":0,"unique_visitors":0,"recent":[],"token_calls":[{"caller":"abcdef0123456789","email":"bot@example.com","version":3,"calls":42,"last_call_at":0,"last_status":200},{"caller":"3f9a12bcdeadbeef","version":3,"calls":7,"last_call_at":0,"last_status":500},{"caller":"abc","version":1,"calls":1,"last_call_at":0,"last_status":200}]}`)
	for _, want := range []string{"token calls:", "CALLER", "LAST CALL", "bot@example.com", "caller 3f9a12bc…", "caller abc…"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "abcdef01") {
		t.Fatalf("hash leaked next to email: %q", out)
	}
}

func TestStatsWithoutTokenCalls(t *testing.T) {
	for _, body := range []string{
		`{"slug":"demo","name":"Demo","total_visits":0,"unique_visitors":0,"recent":[]}`,
		`{"slug":"demo","name":"Demo","total_visits":0,"unique_visitors":0,"recent":[],"token_calls":[]}`,
	} {
		if out := statsOutput(t, body); strings.Contains(out, "token calls") {
			t.Fatalf("unexpected section: %q", out)
		}
	}
}
