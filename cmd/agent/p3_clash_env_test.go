package main

import (
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"404-probe/internal/agent"
)

func TestP3ActualCLIClashEnvPresenceAndFlagPriority(t *testing.T) {
	for _, tc := range []struct {
		name      string
		present   bool
		value     string
		arguments []string
		want      string
	}{
		{name: "unset_default", want: agent.DefaultClashAPIURL},
		{name: "explicit_empty_disabled", present: true, want: ""},
		{name: "configured_loopback", present: true, value: "http://127.0.0.1:9191", want: "http://127.0.0.1:9191"},
		{name: "empty_flag_overrides_env", present: true, value: "http://127.0.0.1:9191", arguments: []string{"--sing-box-clash-api="}, want: ""},
		{name: "flag_overrides_disabled_env", present: true, arguments: []string{"--sing-box-clash-api=http://127.0.0.1:9292"}, want: "http://127.0.0.1:9292"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROBE_404_SING_BOX_CLASH_API", tc.value)
			if !tc.present {
				if err := os.Unsetenv("PROBE_404_SING_BOX_CLASH_API"); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PROBE_404_ENABLE_REMOTE_REMOVAL", "false")
			oldFlags, oldArgs := flag.CommandLine, os.Args
			defer func() { flag.CommandLine = oldFlags; os.Args = oldArgs }()
			flag.CommandLine = flag.NewFlagSet("p3-isolated", flag.ContinueOnError)
			flag.CommandLine.SetOutput(io.Discard)
			// Exercise run's actual registrations and parsing, then stop at invalid
			// Runner config before state acquisition, collector or any networking.
			os.Args = append([]string{"p3-isolated", "--server=", "--agent-id=", "--token="}, tc.arguments...)
			if err := run(); err == nil || !strings.Contains(err.Error(), "configuration:") {
				t.Fatalf("did not stop at configuration validation: %v", err)
			}
			if got := flag.Lookup("sing-box-clash-api").Value.String(); got != tc.want {
				t.Fatalf("actual CLI=%q want=%q", got, tc.want)
			}
			t.Setenv("P3_OTHER_ENV", "")
			if got := env("P3_OTHER_ENV", "unchanged"); got != "unchanged" {
				t.Fatal("other env semantics changed")
			}
		})
	}
}
