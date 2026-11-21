package bridge

import (
	"strings"
	"unicode"
)

// Packed text. Servers whose pack draws its own forms or HUD send the layout as text: fields cut
// by colons, most of them for the pack's eyes only, such as
//
//	uhzfJXv3tR:5050:5175:431:258:textures/ui/custom_forms/blue/form_inner_border
//	lTC2UbQ1Bd5487:5305:100|040:054:055:062:§7No rank yet:default
//	...:207:327:100:default::textures/ui/custom_forms/x:polls_hover:§ePolls\nVote on what's next
//
// (an element's id, its place and size, a texture, a font, the text). Every server packs it its
// own way, but the fields a player is meant to read look like text and the others do not, so a
// client without the pack can show the readable ones (packedText) and the first texture as the
// element's picture, and leave out an element with nothing readable (a frame, a background).

// packedFonts are font names packs put in a field.
var packedFonts = map[string]bool{"minecraftten": true, "minecraftseven": true, "minecraft": true,
	"smooth": true, "rune": true, "unicode": true, "default": true}

// looksPacked reports whether s is packed fields rather than text: three colons or more, and a
// field that is a number or a texture path.
func looksPacked(s string) bool {
	if strings.Count(s, ":") < 3 {
		return false
	}
	for _, f := range strings.Split(s, ":") {
		f = strings.TrimSpace(f)
		if isNumberField(f) || strings.HasPrefix(f, "textures/") {
			return true
		}
	}
	return false
}

// packedText is the readable fields of packed text, runs of neighbouring ones joined by the
// colon that was between them, and its first texture path ("" if none).
func packedText(s string) (runs []string, texture string) {
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			if t := strings.TrimSpace(strings.Join(cur, ":")); t != "" {
				runs = append(runs, t)
			}
			cur = nil
		}
	}
	for _, f := range strings.Split(s, ":") {
		t := strings.TrimSpace(f)
		if texture == "" && strings.HasPrefix(t, "textures/") {
			texture = t
		}
		if readableField(t) {
			cur = append(cur, f)
		} else {
			flush()
		}
	}
	flush()
	return runs, texture
}

// readableField reports whether a field of packed text is for a player to read.
func readableField(f string) bool {
	plain := strings.TrimSpace(stripColours(f))
	switch {
	case plain == "", isNumberField(plain), packedFonts[strings.ToLower(plain)]:
		return false
	case strings.Contains(plain, "/") && !strings.ContainsAny(plain, " \n"):
		return false // a path
	case strings.ContainsAny(plain, " \n") || strings.Contains(f, "§"):
		return true // words, or coloured on purpose
	case isIdentifier(plain), isRandomID(plain):
		return false
	}
	return strings.ContainsFunc(plain, unicode.IsLetter)
}

// isNumberField reports whether f is a number or numbers ("5050", "100|040", "-1.5").
func isNumberField(f string) bool {
	if f == "" {
		return false
	}
	digits := false
	for _, r := range f {
		switch {
		case r >= '0' && r <= '9':
			digits = true
		case strings.ContainsRune("|.,+-", r):
		default:
			return false
		}
	}
	return digits
}

// isIdentifier reports whether f is a lower-case name ("default", "polls_hover", "update").
func isIdentifier(f string) bool {
	for _, r := range f {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// isRandomID reports whether f is a made-up id: six letters and digits or more, with digits and
// with upper and lower case letters ("uhzfJXv3tR", "lTC2UbQ1Bd5487").
func isRandomID(f string) bool {
	if len(f) < 6 {
		return false
	}
	var upper, lower, digit bool
	for _, r := range f {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			return false
		}
	}
	return upper && lower && digit
}

// stripColours is s without § codes.
func stripColours(s string) string {
	if !strings.Contains(s, "§") {
		return s
	}
	var b strings.Builder
	skip := false
	for _, r := range s {
		switch {
		case skip:
			skip = false
		case r == '§':
			skip = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// readableText is s for a client without the server's pack: s as it is unless it is packed
// fields, else its readable fields joined by a space ("" if it has none).
func readableText(s string) string {
	if !looksPacked(s) {
		return s
	}
	runs, _ := packedText(s)
	return strings.Join(runs, " ")
}
