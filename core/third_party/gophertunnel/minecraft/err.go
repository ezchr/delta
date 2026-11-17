package minecraft

import (
	"errors"
	"fmt"
	"net"
)

var errBufferTooSmall = errors.New("a message sent was larger than the buffer used to receive the message into")

// wrap wraps the error passed into a net.OpError with the op as operation and returns it, or nil if the error
// passed is nil.
func (conn *Conn) wrap(err error, op string) error {
	if err == nil {
		return nil
	}
	return &net.OpError{
		Op:     op,
		Net:    "minecraft",
		Source: conn.LocalAddr(),
		Addr:   conn.RemoteAddr(),
		Err:    err,
	}
}

// DisconnectError is an error returned by operations from Conn when the connection is closed by the other
// end through a packet.Disconnect. It is wrapped in a net.OpError and may be obtained using
// errors.Unwrap(net.OpError).
type DisconnectError string

// Error returns the message held in the packet.Disconnect.
func (d DisconnectError) Error() string {
	return string(d)
}

// DisconnectReason is the error of a connection the other end closed with a packet.Disconnect: the
// packet's reason code and its message. A server that hides the disconnection screen sends no
// message at all, and the code is then all there is to say why. It unwraps to the DisconnectError
// with the message, so errors.As finds either.
type DisconnectReason struct {
	Code    int32
	Message string
}

// Error returns the message, or the reason code when the packet had none.
func (d *DisconnectReason) Error() string {
	if d.Message != "" {
		return d.Message
	}
	return fmt.Sprintf("disconnected by the server without a message (reason code %d)", d.Code)
}

// Unwrap returns the DisconnectError holding the message.
func (d *DisconnectReason) Unwrap() error { return DisconnectError(d.Message) }
