package app

import "testing"

func TestParseSlash(t *testing.T) {
	cases := []struct {
		in      string
		wantCmd SlashCmd
		wantArg string
		wantOK  bool
	}{
		{":cancel", SlashCancel, "", true},
		{":queue", SlashQueue, "", true},
		{":template echo", SlashTemplate, "echo", true},
		{":interactive", SlashInteractive, "", true},
		{":help", SlashHelp, "", true},
		{":edit", SlashEdit, "", true},
		{":status", SlashStatus, "", true},
		{":bogus", SlashUnknown, "bogus", true},
		{"plain prompt", SlashNone, "plain prompt", false},
		{":", SlashNone, ":", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			cmd, arg, ok := ParseSlash(tc.in)
			if cmd != tc.wantCmd || arg != tc.wantArg || ok != tc.wantOK {
				t.Errorf("ParseSlash(%q) = (%v,%q,%v); want (%v,%q,%v)", tc.in, cmd, arg, ok, tc.wantCmd, tc.wantArg, tc.wantOK)
			}
		})
	}
}
