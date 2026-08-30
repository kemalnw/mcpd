package process

import "testing"

func TestLooksLikePromptOnlyUsesCurrentTerminalLine(t *testing.T) {
	tests := []struct {
		name string
		tail string
		want bool
	}{
		{name: "shell prompt", tail: "normal output\n$ ", want: true},
		{name: "python prompt", tail: "Python 3\n>>> ", want: true},
		{name: "mysql prompt", tail: "banner\nmysql> ", want: true},
		{name: "historical dollar", tail: "build step $ \nstill running\n", want: false},
		{name: "historical price", tail: "price $ \nnext\n", want: false},
		{name: "historical hash", tail: "phase # \ncontinuing", want: false},
		{name: "historical percent with carriage return", tail: "progress % \rworking", want: false},
		{name: "completed prompt-like line", tail: "$ \n", want: false},
		{name: "ordinary output", tail: "done\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikePrompt(tt.tail); got != tt.want {
				t.Fatalf("looksLikePrompt(%q) = %v, want %v", tt.tail, got, tt.want)
			}
		})
	}
}

func TestPromptStabilityDelayIsAdaptive(t *testing.T) {
	if got := promptStabilityDelayFor("python3 -q -i"); got != promptStabilityDelayKnown {
		t.Fatalf("known REPL delay=%s want %s", got, promptStabilityDelayKnown)
	}
	if got := promptStabilityDelayFor("bash -i"); got != promptStabilityDelayKnown {
		t.Fatalf("interactive shell delay=%s want %s", got, promptStabilityDelayKnown)
	}
	if got := promptStabilityDelayFor("printf 'build step $ '"); got != promptStabilityDelayUnknown {
		t.Fatalf("unknown command delay=%s want %s", got, promptStabilityDelayUnknown)
	}
}
