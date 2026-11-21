# Form layout adapters (docs/FORM_LAYOUTS.md)

Optional, per server. Without the file described here nothing changes.

## What it is for

Many Bedrock servers draw their menus with JSON UI: the server sends an ordinary form (an action
form with buttons) and its resource pack (`ui/server_form.json`) redraws it as a screen of its
own. The layout is packed into the form's texts: the title carries the canvas, and every
"button" is really a panel, a text or a clickable button with its position, size and texture
written into its label. A Java player would see that raw data as button labels.

Every server invents its own encoding, so Delta cannot decode it by itself. With an adapter the
server owner describes **his** encoding once, and Delta shows the form to Java players as a
tidy dialog laid out like the Bedrock screen:

* **decorations** (panels, borders, backgrounds) are dropped;
* **texts** become the dialog's body, in reading order (top to bottom, left to right);
* **buttons** become a grid: buttons at about the same height are one row, ordered left to
  right; the dialog has as many columns as the longest row; a button of a shorter row goes under
  the nearest button of the longest row; each button is as wide as its decoded width allows;
* a button's **label** is the first line of the decoded label, the following lines are its
  **tooltip** (shown on hover), and its **texture** is its picture (the same small icon any form
  button's image gets, when the converted resource pack has that texture and `form_icons` is on);
* a click answers the server with the button's **original index** in the form, exactly what a
  Bedrock client sends.

It stays a normal Java dialog: the server's artwork is not drawn, positions are kept only as rows
and columns.

## The file

`plugins/delta/form_layouts.json` (Delta's data folder, next to `config.toml`). Another path:
`form_layouts = "C:/path/file.json"` under `[core.options]`; `form_layouts = "off"` ignores the
file. It is read when the server starts; restart after editing. Mistakes are logged as
`form layouts: ...` and only the adapter that has one is left out.

```json
{
  "layouts": [
    {
      "name": "abyss",
      "title": "^dynamic:(?P<width>\\d+):(?P<height>\\d+):[\\d:]*\\|[\\d:]*:(?P<title>[^:]+):textures/",
      "units": {
        "x": {"offset": -5000, "scale": 0.1},
        "y": {"offset": -5000, "scale": 0.1}
      },
      "max_width": 400,
      "unmatched": "drop",
      "elements": [
        {"kind": "decoration", "match": "^\\w{10}:(?P<x>\\d+):(?P<y>\\d+):(?P<w>\\d+):(?P<h>\\d+):(?P<texture>textures/[^:]+)$"},
        {"kind": "text", "match": "(?s)^\\w{10}(?P<x>\\d{4}):(?P<y>\\d+):(?P<w>\\d+)\\|[\\d:]*:(?P<label>.*):default$"},
        {"kind": "button", "match": "(?s)^\\w{10}:(?P<x>\\d+):(?P<y>\\d+):(?P<w>\\d+):default::(?P<texture>[^:]*):(?P<hover>[^:]*):(?P<label>.*)$"}
      ]
    }
  ]
}
```

Expressions are Go regular expressions ([RE2 syntax](https://github.com/google/re2/wiki/Syntax):
named groups are `(?P<name>...)`, no look-ahead or back-references). In JSON every backslash is
doubled. They are not anchored: use `^` and `$`. `(?s)` lets `.` match a line break, which a
label of several lines needs.

### An adapter

| Key | Meaning |
|---|---|
| `name` | For the log. |
| `title` | **Required.** Picks the form: the form's title has to match. Only action forms (`"type": "form"`) are looked at. Named groups: `title` = what the player reads as the dialog's title (without the group the whole title is shown); `width` = the canvas width, used for the automatic `scale`; `height` is accepted and not used. The first adapter in the file whose `title` matches is used. |
| `elements` | The rules for the form's buttons, tried in order; the first whose `match` matches a button's text says what it is. |
| `units` | How the numbers of the groups `x`, `y`, `w`, `h` become pixels: `(number + offset) * scale` (`scale` 1 and `offset` 0 when left out). |
| `scale` | GUI pixels of the Java dialog per pixel of the encoding. Left out: 1, or `max_width / width` when the title's `width` is bigger than `max_width`. |
| `max_width` | The most GUI pixels a row of buttons may take (default 400, at most 1024). A row that is wider shrinks, all buttons alike (never below 24 pixels each). Java's window is at least 320 GUI pixels wide and usually 480 to 960. |
| `row_tolerance` | Buttons whose tops are this many GUI pixels apart or less are one row (default 4). Buttons that overlap in height (when `h` is decoded) are one row too. |
| `unmatched` | A button no rule matches: `"button"` (default: it stays a button with its text as sent, after the grid) or `"drop"`. |
| `content` | `false`: the form's own content text is not shown (default: it is the first body paragraph when not empty). |

### An element rule

| Key | Meaning |
|---|---|
| `kind` | `"decoration"` (dropped), `"text"` (body) or `"button"`. |
| `match` | The expression. Named groups, all optional: `x`, `y` (top left), `w`, `h` (size), `texture` (a texture path of the server's pack; empty or `default` = none), `label` (the text; for a button, lines after the first are the tooltip). Other group names are ignored, so naming a part (`hover`) for readability is fine. |
| `units` | Replaces the adapter's `units` for this rule. |
| `defaults` | Values for groups the expression does not have or that matched nothing, as strings in the encoding's units: `{"w": "100"}`. |

A button without `x` and `y` is placed after the grid in the form's order; without `w` it is a
plain 150 pixel button. A dropped element still counts for the index: the server gets the number
the clicked button has in the form it sent.

## The worked example ("Abyss Menu")

The server sends the title

```
dynamic:440:280:440:011:500:489:506:515:000:000:500:500:000|100:113:114:159:160:196:197:200:201:208:Abyss Menu:textures/...
```

and buttons like

```
uhzfJXv3tR:5050:5175:431:258:textures/ui/custom_forms/blue/form_inner_border
aDif2XyYsN:5070:5195:427:254:textures/ui/custom_forms/blue/drift
lTC2UbQ1Bd5487:5305:100|040:054:055:062:§7No rank yet:default
pQ7mZk2LxA:5207:5327:100:default::textures/ui/custom_forms/blue/polls:polls_hover:§ePolls
Vote on what's next
```

How the adapter above reads them:

* **Title**: `dynamic:` then the canvas, 440 x 280 (`width`, `height`); after the `|` and its
  numbers, `Abyss Menu` is the `title`. The canvas is wider than `max_width` 400, so the scale is
  400/440.
* **Panels** (first rule): a 10 character id, `x:y:w:h` and a texture path. `5050` and `5175`
  are tenths of a pixel above 5000, hence `units` `offset -5000, scale 0.1`: x 5, y 17.5; the
  size is 431 x 258 pixels as written. Dropped.
* **Text** (second rule): the id runs straight into the 4 digit x (`5487` = 48.7), then y
  (`5305` = 30.5), a width, numbers, the text and `default`. `§7No rank yet` goes to the body.
* **Buttons** (third rule): id, x, y, width, `default`, an empty field, the texture, the hover
  texture and the label. `§ePolls` is the label, `Vote on what's next` the tooltip, `.../polls`
  the picture. Buttons with y 5327 and 5330 are one row (0.3 pixels apart); one at y 5900 starts
  the next row and sits under the button of the first row whose middle is nearest.
* Anything else is dropped (`"unmatched": "drop"`).

The start of the real buttons' strings was not known when this was written (`...:207:327:100:
default::textures/...:polls_hover:§ePolls`), so the third expression is a best guess: compare it
with what your server sends and adjust. This example is also the test
`core/bridge/forms_layout_test.go` (file `core/bridge/testdata/form_layouts_abyss.json`).

## Finding out what the server sends

Set `debug = true` under `[core]` and open the menu as a Java player without an adapter: the raw
texts are the dialog's title and button labels. Write one rule per shape of string, most
specific first. With the adapter in place the log line `form shown by a layout adapter` (debug)
names the adapter and the grid.

## Limits

* The Bedrock artwork (panels, button faces, hover pictures) is not drawn; a button's texture is
  a 16 pixel icon before its label, only for textures the converted pack has.
* A dialog's grid has equal rows: a button cannot span columns or rows, and gaps in a row are
  thin empty places (clicking one closes the form, as Escape does).
* Texts are all above the buttons, whatever their place among them on Bedrock.
* Only action forms are adapted; modal and custom forms are shown as before.
