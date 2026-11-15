package protocol

import (
	"errors"
	"regexp"
	"strings"
)

const MaxMessageBytes = 64 << 10

var (
	ErrInvalidMessage = errors.New("invalid message")
	uidPattern        = regexp.MustCompile(`^[A-Za-z0-9_.]{1,128}$`)
)

type Message struct {
	Target string
	Text1  string
	Text2  string
}

func Parse(raw string) (Message, error) {
	if raw == "" || len(raw) > MaxMessageBytes {
		return Message{}, ErrInvalidMessage
	}
	parts := strings.SplitN(raw, "-", 3)
	if len(parts) != 3 || !validAddress(parts[0]) || parts[1] == "" {
		return Message{}, ErrInvalidMessage
	}
	return Message{Target: parts[0], Text1: parts[1], Text2: parts[2]}, nil
}

func (m Message) String() string { return m.Target + "-" + m.Text1 + "-" + m.Text2 }

func ValidUID(uid string) bool {
	return uid != "0" && !strings.HasPrefix(uid, "_http.") && uidPattern.MatchString(uid)
}

func validAddress(value string) bool {
	return ValidUID(value) || validVirtualUID(value)
}

func validVirtualUID(value string) bool {
	return strings.HasPrefix(value, "_http.") && len(value) > len("_http.") && uidPattern.MatchString(value)
}
