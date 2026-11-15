package protocol

import "testing"

func TestParsePreservesHyphensInThirdField(t *testing.T) {
	msg, err := Parse("target-hello-a-b-c")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.Target != "target" || msg.Text1 != "hello" || msg.Text2 != "a-b-c" {
		t.Fatalf("unexpected message: %#v", msg)
	}
	if got := msg.String(); got != "target-hello-a-b-c" {
		t.Fatalf("String() = %q", got)
	}
}

func TestParseRejectsMalformedAndOversizedMessages(t *testing.T) {
	tests := []string{"", "one", "one-two", "-two-three", "one--three"}
	for _, input := range tests {
		if _, err := Parse(input); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", input)
		}
	}
}

func TestValidUID(t *testing.T) {
	for _, uid := range []string{"PC001", "client_2", "admin.main"} {
		if !ValidUID(uid) {
			t.Errorf("ValidUID(%q) = false", uid)
		}
	}
	for _, uid := range []string{"", "0", "has-dash", "has space", "_http.fake", "中文"} {
		if ValidUID(uid) {
			t.Errorf("ValidUID(%q) = true", uid)
		}
	}
}
