package bridge

import (
	"delta/core/internal/javapack"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ezchr/go-mcjava/text"
	v777 "github.com/ezchr/go-mcjava/v777"
	"github.com/ezchr/go-mcjava/wire"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

// Server forms (milestone 3) are shown as Java dialogs (show_dialog, 1.21.6+), ported from
// javasession's text_form.go but built from the server's form JSON:
//
//	menu ("form")  -> multi_action dialog: content and labels as body, a button per form button
//	modal          -> multi_action dialog with the two buttons side by side
//	custom_form    -> multi_action dialog: labels/headers as body, inputs as dialog inputs
//	                  (input -> text, toggle -> boolean, slider -> number_range, dropdown and
//	                  step_slider -> single_option) and a Done button
//
// Every button runs a dynamic/custom action (delta:form) whose payload has the form id and
// the button plus the input values; the client sends it back as custom_click_action and the
// bridge answers the server's form with ModalFormResponse. Cancel or Escape answers it as closed.
//
// A button's picture ("image": a texture path) is a glyph of one of the converted pack's icon
// fonts (javapack/icons.go) before the label, when the client has that pack and the font has the
// texture; else the label is as it was. A picture from a URL is not shown: a dialog draws text
// and items, and the pack is made and loaded before a form names its pictures.
//
// A form the pack draws as a screen of its own gets none (see there).
//
// A plain menu is one column, as on Bedrock; one of menuColumnsFrom buttons or more whose labels
// are all short is two (menuColumns).

const formAction = ProductName + ":form"

// formState is the forms the server sent that are not answered yet.
type formState struct {
	open map[uint32]*bedrockForm
}

type bedrockForm struct {
	Type     string          `json:"type"`
	Title    formText        `json:"title"`
	Content  json.RawMessage `json:"content"`
	Elements []formElement   `json:"elements"`
	Buttons  []formElement   `json:"buttons"`
	Button1  formText        `json:"button1"`
	Button2  formText        `json:"button2"`
}

type formElement struct {
	Type        string     `json:"type"`
	Text        formText   `json:"text"`
	Image       formImage  `json:"image"`
	Default     any        `json:"default"`
	Placeholder formText   `json:"placeholder"`
	Min         float64    `json:"min"`
	Max         float64    `json:"max"`
	Step        float64    `json:"step"`
	Options     []formText `json:"options"`
	Steps       []formText `json:"steps"`
}

// formImage is a button's picture: a texture path in the server's pack, or a URL.
type formImage struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

// formScreen is the screen the server's pack draws for a form with this title (packs.go), nil
// for an ordinary form.
func (p *Player) formScreen(title string) *javapack.FormScreen {
	for i := range p.formScreens {
		if s := &p.formScreens[i]; strings.Contains(title, s.Marker) {
			return s
		}
	}
	return nil
}

// formIconsOn is [core.options] form_icons: the converted pack has the icon font and buttons
// show their pictures.
var formIconsOn = true

// formButton is a button as the dialog shows it: its label and its picture's glyph (0: none) in
// an icon font.
type formButton struct {
	label string
	font  string
	icon  rune
}

// formIcon is the glyph of a button's picture, 0 when it cannot be shown: no picture or one from
// a URL, a texture the pack's font has not, or a client that did not load the pack. p.mu is held.
func (p *Player) formIcon(img formImage) (font string, icon rune) {
	if !formIconsOn || img.Data == "" || strings.EqualFold(img.Type, "url") {
		return "", 0
	}
	jp := p.packFor()
	if jp == nil || p.packs.sent == nil || p.packs.sent.sha != jp.sha || p.packs.status != packLoaded {
		return "", 0
	}
	font, icon, _ = jp.res.Icon(img.Data)
	return font, icon
}

// iconLabel is a button's label with its picture, glyph icon of font, before it.
func iconLabel(label text.Component, font string, icon rune) text.Component {
	if icon == 0 || font == "" {
		return label
	}
	// White: the glyph is the picture's own colours whatever the label's colour is.
	return text.Component{Extra: []text.Component{
		{Text: string(icon), Font: font, Color: "white"}, {Text: " "}, label}}
}

// A plain menu with this many buttons or more goes in two columns when every label fits half
// the width: a dialog shows about seven buttons before it scrolls.
const menuColumnsFrom = 8

// menuColumns is how many columns a plain menu's buttons are in: two when there are
// menuColumnsFrom or more and each label is one line that fits a button of the client's own
// width (150 pixels: 22 letters, 19 beside a picture), else one, as Bedrock has them.
func menuColumns(buttons []formButton) int32 {
	if len(buttons) < menuColumnsFrom {
		return 1
	}
	for _, b := range buttons {
		room := 22
		if b.icon != 0 {
			room = 19
		}
		if strings.ContainsAny(b.label, "\n\r") || utf8.RuneCountInString(b.label)-2*strings.Count(b.label, "§") > room {
			return 1
		}
	}
	return 2
}

func init() {
	OnBedrock(packet.IDModalFormRequest, (*Player).showForm)
	OnBedrock(packet.IDClientBoundCloseForm, func(p *Player, pk *packet.ClientBoundCloseForm) {
		clear(p.win.forms.open)
		p.send(v777.ClientboundPlayClearDialog, p.pk())
	})
	OnJavaStop(v777.ServerboundPlayCustomClickAction, func(p *Player, r *wireReader) (bool, error) {
		id := r.String(32767)
		if r.Err != nil || id != formAction {
			return false, nil // someone else's
		}
		p.formClicked(r.Rest())
		return true, nil
	})
}

// bedrockText converts Bedrock-formatted text (§ codes).
func bedrockText(s string) text.Component { return text.Legacy(s, text.Bedrock) }

// showForm shows a server form as a dialog.
func (p *Player) showForm(pk *packet.ModalFormRequest) {
	if p.packPending() {
		// A dialog replaces the resource pack prompt, which the client takes as "No": the form
		// waits until the player answered (packStatus), a server that opens one on join included.
		held := *pk
		held.FormData = append([]byte(nil), pk.FormData...)
		if len(p.packs.heldForms) < 10 {
			p.packs.heldForms = append(p.packs.heldForms, &held)
		}
		return
	}
	var bf bedrockForm
	if err := json.Unmarshal(pk.FormData, &bf); err != nil {
		p.log.Info("bad form from the server", "err", err)
		p.Write(&packet.ModalFormResponse{FormID: pk.FormID, CancelReason: protocol.Option[uint8](packet.ModalFormCancelReasonUserClosed)})
		return
	}
	fs := &p.win.forms
	if fs.open == nil {
		fs.open = map[uint32]*bedrockForm{}
	}
	if len(fs.open) >= 10 {
		for k := range fs.open {
			delete(fs.open, k)
			break
		}
	}
	fs.open[pk.FormID] = &bf
	id := int32(pk.FormID)

	w := p.pk()
	w.VarInt(0) // a dialog given inline
	w.Byte(text.TagCompound)
	text.StringTag(w, "type", "minecraft:multi_action")
	// A form the pack draws as a screen of its own: its body is packed data for that layout, so
	// the title, the text and the buttons' names come from the layout (javapack.FormScreen).
	screen := p.formScreen(string(bf.Title))
	if screen != nil && bf.Type != "form" {
		screen = nil
	}
	title := bedrockText(string(bf.Title))
	if screen != nil {
		title = bedrockText(screen.PlainTitle())
	}
	text.ComponentTag(w, "title", &title)
	text.BoolTag(w, "pause", false)
	text.StringTag(w, "after_action", "close")

	var body []string
	var buttons []formButton
	var buttonIndex []int32 // the form's number of each button shown (nil: its place in buttons)
	columns, width := int32(1), int32(0)
	var inputs []formElement
	switch bf.Type {
	case "form":
		var content string
		_ = json.Unmarshal(bf.Content, &content)
		elems := bf.Buttons
		if len(elems) == 0 {
			elems = bf.Elements
		}
		if screen != nil {
			// The screen's buttons are the form's by their number among its buttons.
			var in []javapack.FormButtonIn
			for _, e := range elems {
				if e.Type == "" || e.Type == "button" {
					in = append(in, javapack.FormButtonIn{Text: string(e.Text), Image: e.Image.Data})
				}
			}
			view := screen.View(javapack.NewFormData(string(bf.Title), content), in)
			body = view.Body
			buttonIndex = []int32{}
			// No pictures here: on a screen of the pack's own a button's image is what the layout
			// makes of it, mostly the button's face (a slot's frame), not an icon beside a label.
			for _, b := range view.Buttons {
				buttons = append(buttons, formButton{label: b.Label})
				buttonIndex = append(buttonIndex, int32(b.Index))
			}
			columns, width = int32(view.Columns), int32(view.Width)
			break
		}
		body = appendBody(body, content)
		for _, e := range elems {
			switch e.Type {
			case "", "button":
				font, icon := p.formIcon(e.Image)
				buttons = append(buttons, formButton{string(e.Text), font, icon})
			case "label", "header":
				body = appendBody(body, string(e.Text))
			}
		}
		columns = menuColumns(buttons)
	case "modal":
		var content string
		_ = json.Unmarshal(bf.Content, &content)
		body = appendBody(body, content)
		buttons = []formButton{{label: string(bf.Button1)}, {label: string(bf.Button2)}}
	case "custom_form":
		_ = json.Unmarshal(bf.Content, &inputs)
		for _, e := range inputs {
			if e.Type == "label" || e.Type == "header" {
				body = appendBody(body, string(e.Text))
			}
		}
	}
	writeBody(w, body)
	if bf.Type == "custom_form" {
		writeInputs(w, inputs)
		text.ListStart(w, "actions", text.TagCompound, 1)
		writeButton(w, text.Translatable("gui.done"), id, -1)
	} else {
		if len(buttons) == 0 {
			buttons = []formButton{{}}
		}
		text.ListStart(w, "actions", text.TagCompound, len(buttons))
		for i, b := range buttons {
			n := int32(i)
			if buttonIndex != nil {
				n = buttonIndex[i]
			}
			writeButtonWidth(w, iconLabel(labelText(b.label), b.font, b.icon), id, n, width)
		}
	}
	if bf.Type == "modal" {
		columns = 2
	}
	text.IntTag(w, "columns", columns)
	text.Key(w, text.TagCompound, "exit_action")
	writeButton(w, text.Translatable("gui.cancel"), id, -2)
	w.Byte(text.TagEnd)
	p.send(v777.ClientboundPlayShowDialog, w)
	p.log.Debug("form shown", "id", pk.FormID, "type", bf.Type, "buttons", len(buttons), "columns", columns, "inputs", len(inputs))
}

var javaKeys = map[string]bool{"gui.yes": true, "gui.no": true, "gui.ok": true, "gui.cancel": true,
	"gui.done": true, "gui.back": true, "gui.close": true}

func labelText(s string) text.Component {
	if javaKeys[s] {
		return text.Translatable(s)
	}
	return bedrockText(s)
}

func appendBody(body []string, t string) []string {
	if t == "" {
		return body
	}
	return append(body, t)
}

func writeBody(w *wire.Writer, body []string) {
	if len(body) == 0 {
		return
	}
	text.ListStart(w, "body", text.TagCompound, len(body))
	for _, b := range body {
		text.StringTag(w, "type", "minecraft:plain_message")
		c := bedrockText(b)
		text.ComponentTag(w, "contents", &c)
		text.IntTag(w, "width", 300)
		w.Byte(text.TagEnd)
	}
}

func isInput(t string) bool {
	switch t {
	case "input", "toggle", "slider", "dropdown", "step_slider":
		return true
	}
	return false
}

// writeInputs writes a custom form's inputs as dialog inputs keyed e<index>.
func writeInputs(w *wire.Writer, elems []formElement) {
	n := 0
	for _, e := range elems {
		if isInput(e.Type) {
			n++
		}
	}
	if n == 0 {
		return
	}
	text.ListStart(w, "inputs", text.TagCompound, n)
	for i, e := range elems {
		if !isInput(e.Type) {
			continue
		}
		text.StringTag(w, "key", "e"+strconv.Itoa(i))
		label := bedrockText(string(e.Text))
		switch e.Type {
		case "input":
			text.StringTag(w, "type", "minecraft:text")
			text.ComponentTag(w, "label", &label)
			def, _ := e.Default.(string)
			text.StringTag(w, "initial", def)
			text.IntTag(w, "max_length", int32(max(256, len(def))))
			text.IntTag(w, "width", 300)
		case "toggle":
			text.StringTag(w, "type", "minecraft:boolean")
			text.ComponentTag(w, "label", &label)
			def, _ := e.Default.(bool)
			text.BoolTag(w, "initial", def)
		case "slider":
			text.StringTag(w, "type", "minecraft:number_range")
			text.ComponentTag(w, "label", &label)
			lo, hi := e.Min, e.Max
			if hi <= lo {
				hi = lo + 1
			}
			text.FloatTag(w, "start", float32(lo))
			text.FloatTag(w, "end", float32(hi))
			if def, ok := e.Default.(float64); ok {
				text.FloatTag(w, "initial", float32(min(max(def, lo), hi)))
			}
			if e.Step > 0 {
				text.FloatTag(w, "step", float32(e.Step))
			}
			text.IntTag(w, "width", 300)
		case "dropdown", "step_slider":
			opts := e.Options
			if e.Type == "step_slider" {
				opts = e.Steps
			}
			if len(opts) == 0 {
				opts = []formText{""}
			}
			def := 0
			if d, ok := e.Default.(float64); ok {
				def = int(d)
			}
			text.StringTag(w, "type", "minecraft:single_option")
			text.ComponentTag(w, "label", &label)
			text.ListStart(w, "options", text.TagCompound, len(opts))
			for j, o := range opts {
				text.StringTag(w, "id", strconv.Itoa(j))
				c := bedrockText(string(o))
				text.ComponentTag(w, "display", &c)
				if j == def {
					text.BoolTag(w, "initial", true)
				}
				w.Byte(text.TagEnd)
			}
			text.IntTag(w, "width", 300)
		}
		w.Byte(text.TagEnd)
	}
}

// writeButton writes a button compound's entries and its end: a label and a dynamic/custom
// action whose payload is {f: id, b: button} (b -1: submit, -2: closed).
func writeButton(w *wire.Writer, label text.Component, id, button int32) {
	writeButtonWidth(w, label, id, button, 0)
}

// writeButtonWidth is writeButton for a button width pixels wide (0: the client's 150).
func writeButtonWidth(w *wire.Writer, label text.Component, id, button, width int32) {
	text.ComponentTag(w, "label", &label)
	if width > 0 {
		text.IntTag(w, "width", width)
	}
	text.Key(w, text.TagCompound, "action")
	text.StringTag(w, "type", "minecraft:dynamic/custom")
	text.StringTag(w, "id", formAction)
	text.Key(w, text.TagCompound, "additions")
	text.IntTag(w, "f", id)
	text.IntTag(w, "b", button)
	w.Byte(text.TagEnd)
	w.Byte(text.TagEnd)
	w.Byte(text.TagEnd)
}

// formClicked answers a form from a dialog button.
func (p *Player) formClicked(payload []byte) {
	vals, err := readClickPayload(payload)
	if err != nil {
		p.log.Debug("form click payload", "err", err)
		return
	}
	fid, _ := vals["f"].(int32)
	button, _ := vals["b"].(int32)
	fs := &p.win.forms
	f, ok := fs.open[uint32(fid)]
	if !ok || p.rem == nil {
		return
	}
	delete(fs.open, uint32(fid))
	resp, err := formResponse(f, button, vals)
	if err != nil {
		p.log.Debug("form response", "err", err)
		resp = nil
	}
	if resp == nil {
		p.Write(&packet.ModalFormResponse{FormID: uint32(fid), CancelReason: protocol.Option[uint8](packet.ModalFormCancelReasonUserClosed)})
		return
	}
	p.log.Debug("form answered", "id", fid, "response", string(resp))
	p.Write(&packet.ModalFormResponse{FormID: uint32(fid), ResponseData: protocol.Option(resp)})
}

// formResponse builds the server's response JSON: nil when closed, the button index for a
// menu, true/false for a modal, the values for a custom form.
func formResponse(f *bedrockForm, button int32, vals map[string]any) ([]byte, error) {
	if button == -2 {
		return nil, nil
	}
	switch f.Type {
	case "form":
		if button < 0 {
			return nil, errors.New("menu answered without a button")
		}
		return json.Marshal(button)
	case "modal":
		return json.Marshal(button == 0)
	case "custom_form":
		var elems []formElement
		_ = json.Unmarshal(f.Content, &elems)
		out := make([]any, len(elems))
		for i, e := range elems {
			v, ok := vals["e"+strconv.Itoa(i)]
			if !ok {
				v = defaultValue(e)
			}
			switch e.Type {
			case "input":
				s, _ := v.(string)
				out[i] = s
			case "toggle":
				b, _ := v.(int8)
				out[i] = b != 0
			case "slider":
				fv, _ := v.(float32)
				if math.IsNaN(float64(fv)) || math.IsInf(float64(fv), 0) {
					return nil, errors.New("slider value is not a finite number")
				}
				x := float64(fv)
				if e.Step > 0 {
					x = e.Min + math.Round((x-e.Min)/e.Step)*e.Step
				}
				out[i] = min(max(x, e.Min), e.Max)
			case "dropdown", "step_slider":
				s, _ := v.(string)
				n, _ := strconv.Atoi(s)
				out[i] = n
			default:
				out[i] = nil
			}
		}
		return json.Marshal(out)
	}
	return nil, errors.New("unknown form type " + f.Type)
}

// defaultValue is an input's value when the client sent none, in the payload's types.
func defaultValue(e formElement) any {
	switch e.Type {
	case "input":
		s, _ := e.Default.(string)
		return s
	case "toggle":
		if b, _ := e.Default.(bool); b {
			return int8(1)
		}
		return int8(0)
	case "slider":
		d, ok := e.Default.(float64)
		if !ok {
			d = e.Min
		}
		return float32(d)
	case "dropdown", "step_slider":
		d, _ := e.Default.(float64)
		return strconv.Itoa(int(d))
	}
	return nil
}

// readClickPayload reads a custom_click_action payload: a VarInt length, then optional network
// NBT (TagEnd: none), a compound of the button's additions and the inputs' values.
func readClickPayload(b []byte) (map[string]any, error) {
	r := wire.NewReader(b)
	if n := r.VarInt(); r.Err != nil || n < 0 || int(n) != r.Len() {
		return nil, errors.New("payload length does not match")
	}
	t := r.Byte()
	if r.Err != nil || t == text.TagEnd {
		return map[string]any{}, nil
	}
	if t != text.TagCompound {
		return nil, errors.New("payload is not a compound")
	}
	m := map[string]any{}
	for i := 0; i < 64; i++ {
		tt := r.Byte()
		if r.Err != nil {
			return nil, r.Err
		}
		if tt == text.TagEnd {
			return m, nil
		}
		name := text.ReadString(r)
		switch tt {
		case text.TagByte:
			m[name] = r.Int8()
		case text.TagShort:
			m[name] = int32(r.Int16())
		case text.TagInt:
			m[name] = r.Int32()
		case text.TagFloat:
			m[name] = r.Float32()
		case text.TagDouble:
			m[name] = float32(r.Float64())
		case text.TagString:
			m[name] = text.ReadString(r)
		default:
			return nil, errors.New("unexpected tag type in payload")
		}
		if r.Err != nil {
			return nil, r.Err
		}
	}
	return nil, errors.New("payload too large")
}

var _ = strings.TrimSpace

// formText is a form's text: script forms (the Script API's ActionFormData and friends) send raw
// text objects ({"rawtext":[...]}, {"translate":...}) where command forms send strings. Both
// decode to the text with colour codes.
type formText string

func (t *formText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = formText(s)
		return nil
	}
	*t = formText(rawText(string(b)))
	return nil
}
