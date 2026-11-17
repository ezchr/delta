package bridge

import (
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// TestDialErrorText: a login the server refuses comes with a reason code and no message (the
// disconnection screen is hidden); the player and the log are told what the code means.
func TestDialErrorText(t *testing.T) {
	refusal := func(code int32, msg string) error {
		// As gophertunnel hands it over: in an OpError of the dial.
		return &net.OpError{Op: "dial", Net: "minecraft", Err: &net.OpError{Op: "receive", Net: "minecraft",
			Err: &minecraft.DisconnectReason{Code: code, Message: msg}}}
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{refusal(packet.DisconnectReasonNotAuthenticated, ""), "online-mode=false"},
		{refusal(packet.DisconnectReasonNotAllowed, ""), "allow list"},
		{refusal(packet.DisconnectReasonServerFull, ""), "the server is full"},
		{refusal(packet.DisconnectReasonNoReason, ""), "no reason given (Bedrock disconnect reason "},
		{refusal(packet.DisconnectReasonKicked, "Come back later"), "the server refused: Come back later"},
		{&net.OpError{Op: "dial", Net: "minecraft", Err: minecraft.DisconnectError("An old style refusal")}, "the server refused: An old style refusal"},
	} {
		if got := dialErrorText(c.err); !strings.Contains(got, c.want) {
			t.Errorf("%v: %q, want %q in it", c.err, got, c.want)
		}
	}
	// The message is still found as a DisconnectError (leaveReason and others look for that).
	var de minecraft.DisconnectError
	if err := refusal(packet.DisconnectReasonKicked, "x"); !errors.As(err, &de) || string(de) != "x" {
		t.Errorf("DisconnectError: %q", de)
	}
	if s := refusal(packet.DisconnectReasonNotAuthenticated, "").Error(); !strings.Contains(s, "reason code") {
		t.Errorf("the error says nothing: %q", s)
	}
}
