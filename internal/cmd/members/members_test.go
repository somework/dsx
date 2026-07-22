package members

import (
	"strings"
	"testing"
)

// One condition covered two mistakes and the message described only one of
// them: `(*email == "") == (*uuid == "")` is true when both are given AND when
// neither is, so a caller who simply forgot to name an invitee was told they
// had supplied a duplicate they never typed. The wrong diagnosis is the whole
// cost — the caller reads it and goes looking for the second flag.
func TestMemberAddNamesTheMistakeItActuallyFound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
		not  string
	}{
		{
			name: "neither",
			args: []string{"p1", "--role", "editor"},
			want: "--email or --uuid",
			not:  "not both",
		},
		{
			name: "both",
			args: []string{"p1", "--role", "editor", "--email", "a@b.c", "--uuid", "u1"},
			want: "not both",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := cmdMemberAdd(t.Context(), nil, tc.args)
			if err == nil {
				t.Fatalf("dsx member add %s succeeded", strings.Join(tc.args, " "))
			}
			got := err.Error()
			if !strings.Contains(got, tc.want) {
				t.Errorf("refusal = %q, want it to contain %q", got, tc.want)
			}
			if tc.not != "" && strings.Contains(got, tc.not) {
				t.Errorf("refusal = %q, must not contain %q — nothing was given twice", got, tc.not)
			}
		})
	}
}
