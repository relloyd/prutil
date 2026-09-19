package home

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// setScalar returns src with the scalar at path set to value, and nothing else
// changed: not a comment, a blank line, the order of the keys, or the way any
// other value is written. A key missing on the way is added beneath its
// parent, indented the way the file indents.
//
// It edits the text because the alternative, decoding the document and
// encoding it again, does all of those things to a file somebody wrote by
// hand. The positions the parser records say where to edit; the text itself
// is only ever read a line at a time. A shape it is not sure of is an error,
// and SetNotification reads whatever it produces back before trusting it.
//
// value is written as it is given, so it must be a plain YAML scalar such as
// true or 2m, and the keys must be plain words.
func setScalar(src []byte, path []string, value string) ([]byte, error) {
	if len(path) == 0 {
		return nil, errors.New("no setting named")
	}
	for _, key := range path {
		if !plainKey.MatchString(key) {
			return nil, fmt.Errorf("%q is not a key prutil writes", key)
		}
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("could not read the configuration: %w", err)
	}
	t := newYAMLText(src)

	// An empty file, or one holding only comments, is an empty mapping whose
	// keys start in the first column.
	if doc.Kind == 0 {
		return t.appendLines(t.render(path, value, 0)), nil
	}
	root, err := blockRoot(&doc)
	if err != nil {
		return nil, err
	}
	t.unit = indentUnit(root)

	parent, holder := root, (*yaml.Node)(nil)
	for i, name := range path {
		key, val := lookup(parent, name)
		last := i == len(path)-1
		switch {
		case key == nil:
			lines := t.render(path[i:], value, parent.Column-1)
			if holder == nil {
				return t.appendLines(lines), nil
			}
			return t.insert(t.endOf(parent, holder), lines), nil

		case last:
			if err := t.replace(key, val, value); err != nil {
				return nil, fmt.Errorf("%s: %w", dotted(path[:i+1]), err)
			}
			return t.bytes(), nil

		case val.Kind == yaml.MappingNode && val.Style&yaml.FlowStyle == 0 && val.Anchor == "":
			parent, holder = val, key

		case isEmpty(val):
			if err := t.clear(key, val); err != nil {
				return nil, fmt.Errorf("%s: %w", dotted(path[:i+1]), err)
			}
			return t.insert(key.Line, t.render(path[i+1:], value, key.Column-1+t.unit)), nil

		default:
			return nil, fmt.Errorf("%s is not written as one key per line", dotted(path[:i+1]))
		}
	}
	return nil, errors.New("unreachable")
}

// blockRoot is the document's root mapping, which is the only shape prutil
// edits. A flow mapping written on one line, a sequence or a bare scalar is
// somebody else's idea of a configuration file, and walking it as though it
// held keys one per line appends nonsense that only the read-back check in
// applySave would catch — as a refusal the reader cannot act on.
//
// Every entry point goes through here so that they cannot disagree about what
// they will edit, which they did: setScalar checked both the kind and the
// style, setBlockScalar only the kind, and the rest neither.
func blockRoot(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, errors.New("the configuration is not a set of keys")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 {
		return nil, errors.New("the configuration is not a set of keys written one per line")
	}
	return root, nil
}

// plainKey is the shape of a key setScalar will write without quoting.
var plainKey = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// lookup finds a key in a mapping, returning it with its value.
func lookup(mapping *yaml.Node, name string) (key, val *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if k := mapping.Content[i]; k.Kind == yaml.ScalarNode && k.Value == name {
			return k, mapping.Content[i+1]
		}
	}
	return nil, nil
}

// isEmpty reports a value that holds nothing: a key with nothing after it,
// null, or an empty flow mapping.
func isEmpty(n *yaml.Node) bool {
	switch {
	case n.Anchor != "":
		return false
	case n.Kind == yaml.ScalarNode:
		return n.Tag == "!!null"
	case n.Kind == yaml.MappingNode:
		return n.Style&yaml.FlowStyle != 0 && len(n.Content) == 0
	}
	return false
}

// indentUnit is how far the file indents a nested mapping, read from the
// first one it has, or two spaces when it has none.
func indentUnit(root *yaml.Node) int {
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		if val.Kind == yaml.MappingNode && val.Style&yaml.FlowStyle == 0 && val.Column > key.Column {
			return val.Column - key.Column
		}
	}
	return 2
}

// yamlText is a configuration file as lines, which is the unit every edit is
// made in. Each line keeps whatever ended it apart from the newline itself,
// so a file written with CRLF endings keeps them on every line it had.
type yamlText struct {
	lines []string
	// crlf is whether the file ends its lines with CRLF, which new lines copy.
	crlf bool
	// final is whether the last line was terminated.
	final bool
	// unit is the file's indentation step.
	unit int
}

func newYAMLText(src []byte) *yamlText {
	text := string(src)
	t := &yamlText{unit: 2, final: strings.HasSuffix(text, "\n")}
	if text != "" {
		t.lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	}
	t.crlf = len(t.lines) > 0 && strings.HasSuffix(t.lines[0], "\r")
	return t
}

// bytes joins the lines back into a file.
func (t *yamlText) bytes() []byte {
	out := strings.Join(t.lines, "\n")
	if t.final && len(t.lines) > 0 {
		out += "\n"
	}
	return []byte(out)
}

// render writes the keys of path, each nested beneath the one before, with
// value on the last, starting at the given indentation.
func (t *yamlText) render(path []string, value string, indent int) []string {
	out := make([]string, 0, len(path))
	for i, key := range path {
		line := strings.Repeat(" ", indent+i*t.unit) + key + ":"
		if i == len(path)-1 {
			line += " " + value
		}
		out = append(out, t.ending(line))
	}
	return out
}

// ending gives a new line the file's own line ending.
func (t *yamlText) ending(line string) string {
	if t.crlf {
		return line + "\r"
	}
	return line
}

// appendLines adds a top-level block at the end of the file, set off by a
// blank line the way the template separates its sections.
func (t *yamlText) appendLines(lines []string) []byte {
	if n := len(t.lines); n > 0 && strings.TrimSpace(t.lines[n-1]) != "" {
		lines = append([]string{t.ending("")}, lines...)
	}
	t.lines = append(t.lines, lines...)
	t.final = true
	return t.bytes()
}

// insert places lines after the given 1-based line number.
func (t *yamlText) insert(after int, lines []string) []byte {
	after = min(max(after, 0), len(t.lines))
	if after == len(t.lines) {
		// Adding to the end of a file that did not end in a newline would
		// otherwise leave the new lines without one.
		t.final = true
	}
	t.lines = slices.Insert(t.lines, after, lines...)
	return t.bytes()
}

// endOf is the line a new key goes after in a block mapping: beneath the last
// line of its last entry, so it reads as one more of them. When a value runs
// over lines the parser does not account for, such as a block scalar, the key
// goes straight beneath the mapping's own key instead, which is just as valid
// and cannot land inside that value.
func (t *yamlText) endOf(mapping, holder *yaml.Node) int {
	if last, ok := t.lastLine(mapping); ok {
		return last
	}
	return holder.Line
}

// lastLine is the last line a node occupies, when that is certain.
func (t *yamlText) lastLine(n *yaml.Node) (int, bool) {
	switch n.Kind {
	case yaml.ScalarNode:
		if _, _, err := t.token(n); err != nil {
			return 0, false
		}
		return n.Line, true
	case yaml.MappingNode, yaml.SequenceNode:
		if n.Style&yaml.FlowStyle != 0 {
			return 0, false
		}
		last := n.Line
		for i, c := range n.Content {
			if n.Kind == yaml.MappingNode && i%2 == 0 {
				// A key in a block mapping is written on one line.
				last = max(last, c.Line)
				continue
			}
			end, ok := t.lastLine(c)
			if !ok {
				return 0, false
			}
			last = max(last, end)
		}
		return last, true
	}
	return 0, false
}

func (t *yamlText) lastBlockScalarLine(key, val *yaml.Node) int {
	last := val.Line
	baseIndent := key.Column - 1
	for i := val.Line; i < len(t.lines); i++ {
		trimmed := strings.TrimSpace(t.lines[i])
		if trimmed == "" {
			continue
		}
		indent := len(t.lines[i]) - len(strings.TrimLeft(t.lines[i], " "))
		if indent > baseIndent {
			last = i + 1
		} else {
			break
		}
	}
	return last
}

// replace writes value over the scalar val, the value of key.
func (t *yamlText) replace(key, val *yaml.Node, value string) error {
	if val.Kind != yaml.ScalarNode || val.Anchor != "" {
		return errors.New("is not written as a single value")
	}
	if val.Tag == "!!null" && val.Value == "" {
		// Nothing follows the key, so the value goes straight after its colon.
		line, at, err := t.offset(val.Line, val.Column)
		if err != nil || val.Line != key.Line || at == 0 || t.lines[line][at-1] != ':' {
			return errors.New("has no value prutil can find")
		}
		t.lines[line] = t.lines[line][:at] + " " + value + t.lines[line][at:]
		return nil
	}

	line, start, end, err := t.span(val)
	if err != nil {
		return err
	}
	t.lines[line] = t.lines[line][:start] + value + t.lines[line][end:]
	return nil
}

// clear removes an empty value written after key, such as ~ or {}, so that
// lines can be added beneath the key.
func (t *yamlText) clear(key, val *yaml.Node) error {
	if val.Kind == yaml.ScalarNode && val.Value == "" {
		return nil
	}
	if val.Line != key.Line {
		return errors.New("has its empty value on a line of its own")
	}
	line, start, end, err := t.span(val)
	if err != nil {
		return err
	}
	t.lines[line] = strings.TrimRight(t.lines[line][:start], " \t") + t.lines[line][end:]
	return nil
}

// span locates a value on its line as 0-based line index and byte offsets.
func (t *yamlText) span(n *yaml.Node) (line, start, end int, err error) {
	if n.Kind == yaml.MappingNode {
		// Only the empty flow mapping reaches here.
		line, start, err = t.offset(n.Line, n.Column)
		if err != nil {
			return 0, 0, 0, err
		}
		close := strings.IndexByte(t.lines[line][start:], '}')
		if close < 0 || strings.TrimSpace(t.lines[line][start+1:start+close]) != "" {
			return 0, 0, 0, errors.New("has an empty mapping prutil cannot find")
		}
		return line, start, start + close + 1, nil
	}
	start, end, err = t.token(n)
	return n.Line - 1, start, end, err
}

// token finds where a single-line scalar is written: its first byte and the
// byte after its last, within its line. Plain and quoted scalars are the only
// ones it will vouch for.
func (t *yamlText) token(n *yaml.Node) (start, end int, err error) {
	line, start, err := t.offset(n.Line, n.Column)
	if err != nil {
		return 0, 0, err
	}
	text := strings.TrimSuffix(t.lines[line], "\r")

	switch {
	case n.Style&(yaml.LiteralStyle|yaml.FoldedStyle|yaml.TaggedStyle|yaml.FlowStyle) != 0:
		return 0, 0, errors.New("is written in a style prutil does not edit")

	case n.Style&yaml.DoubleQuotedStyle != 0:
		for i := start + 1; i < len(text); i++ {
			switch text[i] {
			case '\\':
				i++
			case '"':
				return start, i + 1, nil
			}
		}

	case n.Style&yaml.SingleQuotedStyle != 0:
		for i := start + 1; i < len(text); i++ {
			if text[i] != '\'' {
				continue
			}
			if i+1 < len(text) && text[i+1] == '\'' {
				i++
				continue
			}
			return start, i + 1, nil
		}

	default:
		end = len(text)
		if c := strings.Index(text[start:], " #"); c >= 0 {
			end = start + c
		}
		if c := strings.Index(text[start:end], "\t#"); c >= 0 {
			end = start + c
		}
		end = start + len(strings.TrimRight(text[start:end], " \t"))
		// A plain value that carries on over the next line reads back as
		// more than this line holds.
		if text[start:end] == n.Value {
			return start, end, nil
		}
	}
	return 0, 0, errors.New("carries on over more than one line")
}

// offset turns the parser's 1-based line and column, which counts characters,
// into a line index and a byte offset.
func (t *yamlText) offset(line, column int) (int, int, error) {
	if line < 1 || line > len(t.lines) || column < 1 {
		return 0, 0, errors.New("is not where the parser says it is")
	}
	text := t.lines[line-1]
	at := 0
	for i := 1; i < column; i++ {
		if at >= len(text) {
			return 0, 0, errors.New("is not where the parser says it is")
		}
		_, size := utf8.DecodeRuneInString(text[at:])
		at += size
	}
	return line - 1, at, nil
}

// renderBlock writes path with value as a block scalar (|-).
func (t *yamlText) renderBlock(path []string, value string, indent int) []string {
	out := make([]string, 0, len(path)+1)
	for i, key := range path {
		line := strings.Repeat(" ", indent+i*t.unit) + key + ":"
		if i == len(path)-1 {
			// The indentation indicator is what makes a template that starts
			// with a space or a tab survive the round trip. A bare |- takes the
			// block's indentation from its first non-empty line, so a first
			// line indented further than the rest puts every later line
			// outside the block, and the file stops parsing. Saying the
			// indentation outright leaves the content's own leading whitespace
			// as content.
			line += fmt.Sprintf(" |%d-", t.unit)
		}
		out = append(out, t.ending(line))
	}
	contentIndent := indent + len(path)*t.unit
	for _, l := range strings.Split(value, "\n") {
		line := ""
		if strings.TrimSpace(l) != "" {
			line = strings.Repeat(" ", contentIndent) + l
		}
		out = append(out, t.ending(line))
	}
	return out
}

// replaceBlock writes a block scalar over key and val.
func (t *yamlText) replaceBlock(key, val *yaml.Node, value string) {
	startLine := key.Line - 1
	endLine := key.Line
	if val.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		endLine = t.lastBlockScalarLine(key, val)
	} else if last, ok := t.lastLine(val); ok {
		endLine = last
	}
	indent := key.Column - 1
	lines := t.renderBlock([]string{key.Value}, value, indent)
	t.lines = slices.Replace(t.lines, startLine, endLine, lines...)
}

// setBlockScalar sets a multiline string at path using YAML literal block scalar style (|-).
func setBlockScalar(src []byte, path []string, value string) ([]byte, error) {
	if len(path) == 0 {
		return nil, errors.New("no setting named")
	}
	for _, key := range path {
		if !plainKey.MatchString(key) {
			return nil, fmt.Errorf("%q is not a key prutil writes", key)
		}
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		t := newYAMLText(src)
		return t.appendLines(t.renderBlock(path, value, 0)), nil
	}

	root, err := blockRoot(&doc)
	if err != nil {
		return nil, err
	}

	t := newYAMLText(src)
	t.unit = indentUnit(root)
	parent, holder := root, (*yaml.Node)(nil)
	for i, name := range path {
		last := i == len(path)-1
		key, val := lookup(parent, name)
		switch {
		case key == nil:
			lines := t.renderBlock(path[i:], value, parent.Column-1)
			if holder == nil {
				return t.appendLines(lines), nil
			}
			return t.insert(t.endOf(parent, holder), lines), nil

		case last:
			t.replaceBlock(key, val, value)
			return t.bytes(), nil

		case val.Kind == yaml.MappingNode && val.Style&yaml.FlowStyle == 0 && val.Anchor == "":
			parent, holder = val, key

		case isEmpty(val):
			if err := t.clear(key, val); err != nil {
				return nil, fmt.Errorf("%s: %w", dotted(path[:i+1]), err)
			}
			return t.insert(key.Line, t.renderBlock(path[i+1:], value, key.Column-1+t.unit)), nil

		default:
			return nil, fmt.Errorf("%s is not written as one key per line", dotted(path[:i+1]))
		}
	}
	return nil, errors.New("unreachable")
}

// setMapEntry adds or updates a key-value entry in the mapping at path.
func setMapEntry(src []byte, path []string, mapKey, mapVal string) ([]byte, error) {
	if len(path) == 0 {
		return nil, errors.New("no setting named")
	}
	for _, key := range path {
		if !plainKey.MatchString(key) {
			return nil, fmt.Errorf("%q is not a key prutil writes", key)
		}
	}
	if strings.TrimSpace(mapKey) == "" {
		return nil, errors.New("map key cannot be empty")
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("could not read the configuration: %w", err)
	}
	t := newYAMLText(src)

	if doc.Kind == 0 {
		lines := t.render(path, "", 0)
		entryLine := strings.Repeat(" ", t.unit) + formatMapKey(mapKey) + ": " + formatMapVal(mapVal)
		lines = append(lines, t.ending(entryLine))
		return t.appendLines(lines), nil
	}
	root, err := blockRoot(&doc)
	if err != nil {
		return nil, err
	}
	t.unit = indentUnit(root)

	parent, holder := root, (*yaml.Node)(nil)
	for i, name := range path {
		key, val := lookup(parent, name)
		last := i == len(path)-1
		switch {
		case key == nil:
			lines := t.render(path[i:], "", parent.Column-1)
			entryLine := strings.Repeat(" ", parent.Column-1+len(path[i:])*t.unit) + formatMapKey(mapKey) + ": " + formatMapVal(mapVal)
			lines = append(lines, t.ending(entryLine))
			if holder == nil {
				return t.appendLines(lines), nil
			}
			return t.insert(t.endOf(parent, holder), lines), nil

		case last:
			if val.Kind == yaml.MappingNode && val.Style&yaml.FlowStyle == 0 {
				entryKey, entryVal := lookup(val, mapKey)
				if entryKey != nil {
					if err := t.replace(entryKey, entryVal, formatMapVal(mapVal)); err != nil {
						return nil, err
					}
					return t.bytes(), nil
				}
				line := strings.Repeat(" ", val.Column-1) + formatMapKey(mapKey) + ": " + formatMapVal(mapVal)
				return t.insert(t.endOf(val, key), []string{t.ending(line)}), nil
			}
			if isEmpty(val) {
				if err := t.clear(key, val); err != nil {
					return nil, err
				}
				line := strings.Repeat(" ", key.Column-1+t.unit) + formatMapKey(mapKey) + ": " + formatMapVal(mapVal)
				return t.insert(key.Line, []string{t.ending(line)}), nil
			}
			return nil, fmt.Errorf("%s is not a mapping", dotted(path))

		case val.Kind == yaml.MappingNode && val.Style&yaml.FlowStyle == 0 && val.Anchor == "":
			parent, holder = val, key

		default:
			return nil, fmt.Errorf("%s is not written as one key per line", dotted(path[:i+1]))
		}
	}
	return nil, errors.New("unreachable")
}

// deleteMapEntry removes an entry from the mapping at path.
func deleteMapEntry(src []byte, path []string, mapKey string) ([]byte, error) {
	if len(path) == 0 {
		return nil, errors.New("no setting named")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("could not read the configuration: %w", err)
	}
	root, err := blockRoot(&doc)
	if err != nil {
		return nil, err
	}
	t := newYAMLText(src)
	t.unit = indentUnit(root)

	parent := root
	for i, name := range path {
		key, val := lookup(parent, name)
		if key == nil || val == nil {
			return src, nil
		}
		last := i == len(path)-1
		if last {
			if val.Kind != yaml.MappingNode {
				return src, nil
			}
			entryKey, entryVal := lookup(val, mapKey)
			if entryKey == nil {
				return src, nil
			}
			t.lines = slices.Delete(t.lines, t.headCommentLine(entryKey), t.endOfEntry(entryKey, entryVal))
			return t.bytes(), nil
		}
		if val.Kind == yaml.MappingNode {
			parent = val
		} else {
			return src, nil
		}
	}
	return src, nil
}

// setSequence sets a list of items at path.
func setSequence(src []byte, path []string, items []string) ([]byte, error) {
	if len(path) == 0 {
		return nil, errors.New("no setting named")
	}
	for _, key := range path {
		if !plainKey.MatchString(key) {
			return nil, fmt.Errorf("%q is not a key prutil writes", key)
		}
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("could not read the configuration: %w", err)
	}
	t := newYAMLText(src)

	renderSeq := func(indent int, keyName string) []string {
		if len(items) == 0 {
			return []string{t.ending(strings.Repeat(" ", indent) + keyName + ": []")}
		}
		lines := []string{t.ending(strings.Repeat(" ", indent) + keyName + ":")}
		for _, item := range items {
			lines = append(lines, t.ending(strings.Repeat(" ", indent+t.unit)+"- "+formatMapVal(item)))
		}
		return lines
	}

	if doc.Kind == 0 {
		lines := make([]string, 0, len(path)+len(items))
		for i, k := range path[:len(path)-1] {
			lines = append(lines, t.ending(strings.Repeat(" ", i*t.unit)+k+":"))
		}
		lines = append(lines, renderSeq((len(path)-1)*t.unit, path[len(path)-1])...)
		return t.appendLines(lines), nil
	}
	root, err := blockRoot(&doc)
	if err != nil {
		return nil, err
	}
	t.unit = indentUnit(root)

	parent, holder := root, (*yaml.Node)(nil)
	for i, name := range path {
		key, val := lookup(parent, name)
		last := i == len(path)-1
		switch {
		case key == nil:
			lines := make([]string, 0, len(path[i:])+len(items))
			for j, k := range path[i : len(path)-1] {
				lines = append(lines, t.ending(strings.Repeat(" ", parent.Column-1+j*t.unit)+k+":"))
			}
			lines = append(lines, renderSeq(parent.Column-1+(len(path[i:])-1)*t.unit, path[len(path)-1])...)
			if holder == nil {
				return t.appendLines(lines), nil
			}
			return t.insert(t.endOf(parent, holder), lines), nil

		case last:
			startLine := key.Line - 1
			endLine := key.Line
			if last, ok := t.lastLine(val); ok {
				endLine = last
			}
			newLines := renderSeq(key.Column-1, key.Value)
			t.lines = slices.Replace(t.lines, startLine, endLine, newLines...)
			return t.bytes(), nil

		case val.Kind == yaml.MappingNode && val.Style&yaml.FlowStyle == 0 && val.Anchor == "":
			parent, holder = val, key

		default:
			return nil, fmt.Errorf("%s is not written as one key per line", dotted(path[:i+1]))
		}
	}
	return nil, errors.New("unreachable")
}

// deleteKey removes the key and value at path.
func deleteKey(src []byte, path []string) ([]byte, error) {
	if len(path) == 0 {
		return nil, errors.New("no setting named")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("could not read the configuration: %w", err)
	}
	root, err := blockRoot(&doc)
	if err != nil {
		return nil, err
	}
	t := newYAMLText(src)

	parent := root
	for i, name := range path {
		key, val := lookup(parent, name)
		if key == nil || val == nil {
			return src, nil
		}
		last := i == len(path)-1
		if last {
			t.lines = slices.Delete(t.lines, t.headCommentLine(key), t.endOfEntry(key, val))
			return t.bytes(), nil
		}
		if val.Kind == yaml.MappingNode {
			parent = val
		} else {
			return src, nil
		}
	}
	return src, nil
}

// endOfEntry is the line index one past the last line a key and its value
// occupy, which is what deleting the whole entry needs.
//
// lastLine will not vouch for a block scalar — token refuses the style — so
// without the second case a multi-line prompt loses its "prompt: |2-" line and
// keeps its body, indented under a mapping that no longer has a key for it.
// That does not parse, which is how the reset ends up refusing every template.
func (t *yamlText) endOfEntry(key, val *yaml.Node) int {
	if val.Kind == yaml.ScalarNode && val.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return t.lastBlockScalarLine(key, val)
	}
	if last, ok := t.lastLine(val); ok {
		return last
	}
	return key.Line
}

// headCommentLine is the line index the key's entry starts at once the comment
// introducing it is counted in, so that deleting the key takes its
// documentation with it rather than leaving it to read as the next key's.
//
// Only an unbroken run of comments at the key's own indentation counts. A blank
// line ends the run: a comment set off from the key by one is as likely to
// belong to the section as to the key, and leaving it is the smaller mistake.
func (t *yamlText) headCommentLine(key *yaml.Node) int {
	start := key.Line - 1
	if key.HeadComment == "" {
		return start
	}
	indent := key.Column - 1
	for i := start - 1; i >= 0; i-- {
		line := strings.TrimSuffix(t.lines[i], "\r")
		trimmed := strings.TrimLeft(line, " ")
		if !strings.HasPrefix(trimmed, "#") || len(line)-len(trimmed) != indent {
			break
		}
		start = i
	}
	return start
}

// plainScalar is a value that means itself written bare: no leading character
// YAML reads as syntax, nothing inside it that ends a scalar, and not one of
// the words YAML resolves to something other than a string.
//
// It is an allowlist because the opposite was tried and leaked: a list of
// characters to quote has to name every one of *&![]{}|>%@`,#'" and the
// indicator rules for each, and the two that are worst are the two that get
// missed. `*star` and `&amp` are still valid YAML — an alias and an anchor —
// so they parse, and the value read back is not the value written.
var plainScalar = regexp.MustCompile(`^[A-Za-z0-9_./~+=-][A-Za-z0-9_./~+= @-]*$`)

// yamlWords are the scalars that read back as something other than the string
// they look like. YAML 1.1 readers, which this one follows for booleans, take
// all of these.
var yamlWords = map[string]bool{
	"y": true, "yes": true, "n": true, "no": true, "true": true, "false": true,
	"on": true, "off": true, "null": true, "nil": true, "~": true,
}

func formatMapKey(k string) string {
	if plainKey.MatchString(k) {
		return k
	}
	return strconv.Quote(k)
}

func formatMapVal(v string) string {
	if !plainScalar.MatchString(v) || yamlWords[strings.ToLower(v)] {
		return strconv.Quote(v)
	}
	// A bare scalar that reads as a number comes back as one, so anything that
	// parses as a number is quoted to stay the string it was given as.
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return strconv.Quote(v)
	}
	return v
}
