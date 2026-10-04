package secretrefs

import "testing"

func TestValidateAllowedUses(t *testing.T) {
	for _, uses := range [][]string{
		nil,
		{"workflow_env"},
		{"wrapped_token"},
		{"image_pull"},
		{"workflow_env", "image_pull"},
	} {
		if err := validateAllowedUses(uses); err != nil {
			t.Errorf("validateAllowedUses(%v) = %v", uses, err)
		}
	}
	if err := validateAllowedUses([]string{"unknown"}); err == nil {
		t.Fatal("unknown allowed_uses value accepted")
	}
}
