package bridge

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/df-mc/dragonfly/server/java/protocol/text"
	v777 "github.com/df-mc/dragonfly/server/java/protocol/v777"
	"github.com/df-mc/dragonfly/server/java/protocol/wire"
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
// Every button runs a dynamic/custom action (javabridge:form) whose payload has the form id and
// the button plus the input values; the client sends it back as custom_click_action and the
// bridge answers the server's form with ModalFormResponse. Cancel or Escape answers it as closed.
// Button images are not shown.

const formAction = ProductName + ":form"

// formState is the forms the server sent that are not answered yet.
type formState struct {
	open map[uint32]*bedrockForm
}

type bedrockForm struct {
	Type     string          `json:"type"`
	Title    string          `json:"title"`
	Content  json.RawMessage `json:"content"`
	Elements []formElement   `json:"elements"`
	Buttons  []formElement   `json:"buttons"`
	Button1  string          `json:"button1"`
	Button2  string          `json:"button2"`
}

type formElement struct {
	Type        string   `json:"type"`
	Text        string   `json:"text"`
	Default     any      `json:"default"`
	Placeholder string   `json:"placeholder"`
	Min         float64  `json:"min"`
	Max         float64  `json:"max"`
	Step        float64  `json:"step"`
	Options     []string `json:"options"`
	Steps       []string `json:"steps"`
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
	title := bedrockText(bf.Title)
	text.ComponentTag(w, "title", &title)
	text.BoolTag(w, "pause", false)
	text.StringTag(w, "after_action", "close")

	var body, buttons []string
	var inputs []formElement
	switch bf.Type {
	case "form":
		var content string
		_ = json.Unmarshal(bf.Content, &content)
		body = appendBody(body, content)
		elems := bf.Buttons
		if len(elems) == 0 {
			elems = bf.Elements
		}
		for _, e := range elems {
			switch e.Type {
			case "", "button":
				buttons = append(buttons, e.Text)
			case "label", "header":
				body = appendBody(body, e.Text)
			}
		}
	case "modal":
		var content string
		_ = json.Unmarshal(bf.Content, &content)
		body = appendBody(body, content)
		buttons = []string{bf.Button1, bf.Button2}
	case "custom_form":
		_ = json.Unmarshal(bf.Content, &inputs)
		for _, e := range inputs {
			if e.Type == "label" || e.Type == "header" {
				body = appendBody(body, e.Text)
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
			buttons = []string{""}
		}
		text.ListStart(w, "actions", text.TagCompound, len(buttons))
		for i, b := range buttons {
			writeButton(w, labelText(b), id, int32(i))
		}
	}
	if bf.Type == "modal" {
		text.IntTag(w, "columns", 2)
	} else {
		text.IntTag(w, "columns", 1)
	}
	text.Key(w, text.TagCompound, "exit_action")
	writeButton(w, text.Translatable("gui.cancel"), id, -2)
	w.Byte(text.TagEnd)
	p.send(v777.ClientboundPlayShowDialog, w)
	p.log.Debug("form shown", "id", pk.FormID, "type", bf.Type, "buttons", len(buttons), "inputs", len(inputs))
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
		label := bedrockText(e.Text)
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
				opts = []string{""}
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
				c := bedrockText(o)
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
	text.ComponentTag(w, "label", &label)
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
