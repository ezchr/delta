package bridge

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Bedrock text helpers copied from the original bridge (text.go): raw JSON text and English
// translations from the vanilla resource pack's texts/en_US.lang (Config.LangFile).

// rawText turns Bedrock raw JSON text ({"rawtext":[...]}) into plain text with colour codes.
func rawText(s string) string {
	if !strings.HasPrefix(strings.TrimSpace(s), "{") {
		return s
	}
	var rt struct {
		RawText []json.RawMessage `json:"rawtext"`
	}
	if json.Unmarshal([]byte(s), &rt) != nil {
		return s
	}
	var b strings.Builder
	for _, part := range rt.RawText {
		b.WriteString(rawPart(part))
	}
	return b.String()
}

func rawPart(data json.RawMessage) string {
	var p struct {
		Text      *string           `json:"text"`
		Translate string            `json:"translate"`
		With      json.RawMessage   `json:"with"`
		RawText   []json.RawMessage `json:"rawtext"`
		Selector  string            `json:"selector"`
		Score     *struct {
			Name      string `json:"name"`
			Objective string `json:"objective"`
		} `json:"score"`
	}
	if json.Unmarshal(data, &p) != nil {
		return ""
	}
	switch {
	case p.Text != nil:
		return *p.Text
	case p.Translate != "":
		var args []string
		if len(p.With) > 0 {
			var list []string
			if json.Unmarshal(p.With, &list) == nil {
				args = list
			} else {
				var obj struct {
					RawText []json.RawMessage `json:"rawtext"`
				}
				if json.Unmarshal(p.With, &obj) == nil {
					for _, a := range obj.RawText {
						args = append(args, rawPart(a))
					}
				}
			}
		}
		return translate(p.Translate, args)
	case len(p.RawText) > 0:
		var b strings.Builder
		for _, part := range p.RawText {
			b.WriteString(rawPart(part))
		}
		return b.String()
	case p.Selector != "":
		return p.Selector
	}
	return ""
}

// The Bedrock English translations (Config.LangFile: the vanilla resource pack's
// texts/en_US.lang). Without that file the copy built into the binary is used (en_US.lang next to
// this file, 1.26.60), so a PC package without it still shows words instead of lang keys.

//go:embed en_US.lang
var embeddedLang []byte

var (
	langOnce sync.Once
	lang     map[string]string
)

func loadLang() {
	lang = map[string]string{}
	var src io.Reader = bytes.NewReader(embeddedLang)
	if f, err := os.Open(cfg.LangFile); err == nil {
		defer f.Close()
		src = f
	}
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "\t#"); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		lang[strings.TrimSpace(k)] = strings.TrimRight(v, " \t\r")
	}
}

var (
	translateKey = regexp.MustCompile(`%[a-zA-Z0-9_.\-]+`)
	positional   = regexp.MustCompile(`%(\d+)\$[sd]|%s|%d`)
)

// translate fills a Bedrock translatable message in English. Keys may be the whole message or
// appear in it ("%key"), as servers send both.
func translate(msg string, params []string) string {
	langOnce.Do(loadLang)
	lang := currentLang() // with the servers' resource pack keys (packs.go)
	params = append([]string(nil), params...)
	for i, p := range params {
		if strings.HasPrefix(p, "%") {
			if v, ok := lang[p[1:]]; ok {
				params[i] = v
			}
		} else if v, ok := lang[p]; ok && strings.Contains(p, ".") {
			params[i] = v
		}
	}
	if strings.HasPrefix(strings.TrimPrefix(msg, "%"), "death.") {
		// A killer's name tag may have several lines (add-ons put hints under the name); a death
		// message is one line.
		for i := range params {
			params[i] = strings.Join(strings.Fields(strings.ReplaceAll(params[i], "\n", " ")), " ")
		}
	}
	if v, ok := lang[strings.TrimPrefix(msg, "%")]; ok {
		msg = v
	} else {
		msg = translateKey.ReplaceAllStringFunc(msg, func(k string) string {
			if v, ok := lang[k[1:]]; ok {
				return v
			}
			return k
		})
	}
	n := 0
	return positional.ReplaceAllStringFunc(msg, func(m string) string {
		idx := n
		if strings.Contains(m, "$") {
			if i, err := strconv.Atoi(m[1 : len(m)-2]); err == nil {
				idx = i - 1
			}
		} else {
			n++
		}
		if idx >= 0 && idx < len(params) {
			return params[idx]
		}
		return ""
	})
}
