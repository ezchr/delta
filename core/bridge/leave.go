package bridge

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Why a server connection ended, in words: "Kicked by <world>: <reason>" for a Disconnect from
// the server (its message translated from the Bedrock lang keys, %disconnect.* and
// disconnectionScreen.*, or the reason code when it sent no message), "Lost connection to
// <world>" when the connection failed, and "You left <world>." for /bb disconnect (commands.go).
//
// gophertunnel handles Disconnect packets itself (they end the connection with a DisconnectError
// holding the message only), so the reason code is caught as the packet arrives
// (minecraft.ReceiveHook).

// disconnectCodes are the reason codes of the Disconnect packets connections got.
var disconnectCodes sync.Map // *minecraft.Conn -> int32

func init() {
	prev := minecraft.ReceiveHook
	minecraft.ReceiveHook = func(conn *minecraft.Conn, id uint32, payload []byte) {
		if prev != nil {
			prev(conn, id, payload)
		}
		if id != packet.IDDisconnect {
			return
		}
		func() {
			defer func() { _ = recover() }()
			var pk packet.Disconnect
			pk.Marshal(protocol.NewReader(bytes.NewReader(payload), 0, false))
			disconnectCodes.Store(conn, pk.Reason)
		}()
	}
}

// leaveReason is what the player reads when the connection ended with err (or a Disconnect
// message).
func (r *Remote) leaveReason(err error, message string) string {
	where := targetName(r.target)
	var de minecraft.DisconnectError
	kicked := message != ""
	if !kicked && errors.As(err, &de) {
		kicked, message = true, string(de)
	}
	code := int32(-1)
	if v, ok := disconnectCodes.LoadAndDelete(r.conn); ok {
		code, kicked = v.(int32), true
	}
	if kicked {
		msg := kickText(code, message)
		r.log.Info("kicked by the server", "reason", msg, "code", code, "message", message)
		return "Kicked by " + where + ": " + msg
	}
	r.log.Info("lost the connection", "err", err)
	if err != nil && timedOut(err) {
		// RakNet and NetherNet end a connection that stopped answering by cancelling it.
		return "Lost connection to " + where + " (the server stopped answering)"
	}
	return "Lost connection to " + where
}

// timedOut reports whether a connection error is the other side not answering.
func timedOut(err error) bool {
	s := err.Error()
	return strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "timed out") ||
		strings.Contains(s, "context canceled")
}

// kickText is a server's disconnect message (or reason code) in English.
func kickText(code int32, message string) string {
	msg := strings.TrimSpace(langText(message))
	if msg == "" || msg == "disconnected" {
		if t := reasonText(code); t != "" {
			return t
		}
	}
	if msg == "" {
		return "no reason given"
	}
	return msg
}

// langKey is a message that is only a lang key (one the lang file does not have).
var langKey = regexp.MustCompile(`^%?[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$`)

// bareKey finds lang keys without % in a message (gophertunnel's dial errors carry them so).
var bareKey = regexp.MustCompile(`%?\b(disconnect|disconnectionScreen)\.[A-Za-z0-9_.]*[A-Za-z0-9_]`)

// langText is a Bedrock message as text: raw JSON text resolved, lang keys translated (whole or
// %key inside), and a key the lang file lacks made readable ("disconnectionScreen.serverFull" ->
// "server full").
func langText(message string) string {
	msg := rawText(translate(message, nil))
	msg = bareKey.ReplaceAllStringFunc(msg, func(k string) string {
		if v, ok := lookupLang(strings.TrimPrefix(k, "%")); ok {
			return v
		}
		return humanKey(k)
	})
	if langKey.MatchString(strings.TrimSpace(msg)) {
		return humanKey(strings.TrimSpace(msg))
	}
	return msg
}

// lookupLang is the English text of a lang key.
func lookupLang(k string) (string, bool) {
	langOnce.Do(loadLang)
	v, ok := lang[k]
	return v, ok
}

// humanKey makes words of a lang key's last part: "%disconnect.serverFull" -> "server full".
func humanKey(k string) string {
	k = strings.TrimPrefix(k, "%")
	if i := strings.LastIndexByte(k, '.'); i >= 0 {
		k = k[i+1:]
	}
	var b strings.Builder
	for i, r := range k {
		if r == '_' {
			b.WriteByte(' ')
			continue
		}
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// dialErrorText is a failed connect's error in words (lang keys in it translated).
func dialErrorText(err error) string {
	var de minecraft.DisconnectError
	if errors.As(err, &de) {
		return "the server refused: " + kickText(-1, string(de))
	}
	if strings.Contains(err.Error(), "deadline exceeded") {
		return "no answer in time"
	}
	if refused(err) {
		return "no Bedrock server is running at that address"
	}
	return langText(err.Error())
}

// reasonText is a Disconnect reason code in words ("" for codes with nothing to add).
func reasonText(code int32) string {
	switch code {
	case packet.DisconnectReasonKicked:
		return "kicked"
	case packet.DisconnectReasonKickedForIdle:
		return "kicked for being idle"
	case packet.DisconnectReasonKickedForExploit:
		return "kicked for cheating"
	case packet.DisconnectReasonServerFull:
		return "the server is full"
	case packet.DisconnectReasonShutdown, packet.DisconnectReasonRequestServerShutdown:
		return "the server shut down"
	case packet.DisconnectReasonTimeout, packet.DisconnectReasonLoadingStateTimeout:
		return "timed out"
	case packet.DisconnectReasonNotAllowed:
		return "you're not on this server's allow list"
	case packet.DisconnectReasonNotAuthenticated, packet.DisconnectReasonNotAuthenticatedFastFail:
		return "the server needs an Xbox Live sign-in"
	case packet.DisconnectReasonVersionMismatch, packet.DisconnectReasonEditionVersionMismatch:
		return "the server runs another Minecraft version"
	case packet.DisconnectReasonInvalidName:
		return "invalid name"
	case packet.DisconnectReasonHostDisconnected, packet.DisconnectReasonHostSignedOut:
		return "the host left"
	case packet.DisconnectReasonHostSuspended:
		return "the host's game was suspended"
	case packet.DisconnectReasonDenyListed:
		return "you're banned from this server"
	case packet.DisconnectReasonStaleConnectionBeingReplaced:
		return "you joined again from somewhere else"
	case packet.DisconnectReasonInvalidPlatformSkin, packet.DisconnectReasonBannedSkin:
		return "the server refused the skin"
	case packet.DisconnectReasonNoPermissions:
		return "no permission"
	case packet.DisconnectReasonBadPacket, packet.DisconnectReasonUnknownPacket, packet.DisconnectReasonUnexpectedPacket:
		return "the server could not read a packet from " + ProductName
	}
	return ""
}
