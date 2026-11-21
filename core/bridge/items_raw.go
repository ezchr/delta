package bridge

import (
	jitem "github.com/ezchr/go-mcjava/item"
	"github.com/ezchr/go-mcjava/version"
	"github.com/ezchr/go-mcjava/wire"
)

// encodeStack writes a Java stack for a client of version v. Components the bridge writes itself
// (Stack.Raw: a book's content, a trim) reach an older client too: the item package knows every
// component's wire form and writes a raw one in the client's (go-mcjava after v0.1.2; before that
// it left them out for an older client and this function put the books back in).
func encodeStack(w *wire.Writer, js *jitem.Stack, v *version.Version) {
	js.EncodeFor(w, jitem.For(v))
}
