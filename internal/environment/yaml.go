package environment

import (
	"fmt"
	"strconv"
	"strings"
)

// A YAML reader that refuses everything it cannot account for.
//
// The declaration is a nested mapping of scalars, plus one sequence of scalars.
// That is the whole language it needs, and writing those hundred lines is
// cheaper than the first external dependency this repository would take (rule 6
// of CLAUDE.md). The cost of a subset is that a reader can write valid YAML this
// does not accept — so every refusal names the construct and the line, rather
// than reporting a value nobody wrote.
//
// What it refuses, by name: tabs, flow style ({} and []), anchors and aliases,
// block scalars (| and >), multiple documents, and a duplicate key. Refusing is
// the whole point: a file somebody mistyped must fail at load, never at the
// first surprising behaviour (#189).

// node is one parsed value: exactly one of the three is set.
//
// scalar and seq are leaves; mapping is the branch. A node whose kind is
// mapping never carries a scalar, which is what lets the schema walk answer
// "this field wanted a value and got a block" with the line to look at.
type node struct {
	kind kind
	// line is where this node was written, for a refusal that can be acted on.
	line int
	// scalar holds the value for kindScalar.
	scalar string
	// seq holds the items for kindSeq, each of them a scalar node.
	seq []node
	// mapping holds the children for kindMapping, and order preserves the
	// file's own so a refusal reports the first mistake rather than a random
	// one.
	mapping map[string]node
	order   []string
}

type kind int

const (
	kindScalar kind = iota
	kindSeq
	kindMapping
)

func (k kind) String() string {
	switch k {
	case kindSeq:
		return "a list"
	case kindMapping:
		return "a block"
	default:
		return "a value"
	}
}

// line is one physical line, already stripped of its comment.
type line struct {
	number int
	indent int
	text   string
}

// parseYAML reads the subset. The returned node is always a mapping; an empty
// document is an empty mapping, which the schema walk then reports as missing
// fields rather than as a parse error.
func parseYAML(src string) (node, error) {
	lines, err := scanLines(src)
	if err != nil {
		return node{}, err
	}
	pos := 0
	root, err := parseBlock(lines, &pos, 0)
	if err != nil {
		return node{}, err
	}
	if pos < len(lines) {
		return node{}, fmt.Errorf("line %d: %q is indented less than the block it is in", lines[pos].number, lines[pos].text)
	}
	if root.kind != kindMapping {
		return node{}, fmt.Errorf("the document must be a block of fields, not %s", root.kind)
	}
	return root, nil
}

// scanLines drops blank lines and comments and records each remaining line's
// indentation. A tab anywhere in the indentation is refused by name: YAML
// forbids it, and the failure it produces otherwise is a mis-nested block that
// reads as a schema error.
func scanLines(src string) ([]line, error) {
	var out []line
	for i, raw := range strings.Split(src, "\n") {
		number := i + 1
		if strings.HasPrefix(raw, "---") || strings.HasPrefix(raw, "...") {
			return nil, fmt.Errorf("line %d: this reader takes one document; %q starts another", number, strings.TrimSpace(raw))
		}
		text := stripComment(raw)
		trimmed := strings.TrimRight(text, " ")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		indent := 0
		for indent < len(trimmed) && trimmed[indent] == ' ' {
			indent++
		}
		if strings.Contains(trimmed[:indent], "\t") || strings.HasPrefix(strings.TrimLeft(trimmed, " "), "\t") {
			return nil, fmt.Errorf("line %d: indent with spaces, not tabs", number)
		}
		out = append(out, line{number: number, indent: indent, text: strings.TrimSpace(trimmed)})
	}
	return out, nil
}

// stripComment removes a # comment, honouring quotes so a # inside a value is
// data. Same reasoning as stripHCLComments in internal/cli: a reader who quotes
// a value containing a hash must get the hash.
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i]
		}
	}
	return s
}

// parseBlock reads every line at exactly `indent` and their children.
func parseBlock(lines []line, pos *int, indent int) (node, error) {
	if *pos >= len(lines) {
		return node{kind: kindMapping, mapping: map[string]node{}}, nil
	}
	if lines[*pos].text[0] == '-' {
		return parseSeq(lines, pos, indent)
	}
	out := node{kind: kindMapping, line: lines[*pos].number, mapping: map[string]node{}}
	for *pos < len(lines) {
		cur := lines[*pos]
		if cur.indent < indent {
			break
		}
		if cur.indent > indent {
			return node{}, fmt.Errorf("line %d: %q is indented further than the field above it, which takes a value", cur.number, cur.text)
		}
		key, rest, ok := strings.Cut(cur.text, ":")
		if !ok {
			return node{}, fmt.Errorf("line %d: %q is not a `key: value` line", cur.number, cur.text)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return node{}, fmt.Errorf("line %d: a field with no name", cur.number)
		}
		if _, dup := out.mapping[key]; dup {
			return node{}, fmt.Errorf("line %d: %q is declared twice; one of the two would win in silence", cur.number, key)
		}
		rest = strings.TrimSpace(rest)
		*pos++
		if rest != "" {
			value, err := scalarNode(rest, cur.number)
			if err != nil {
				return node{}, err
			}
			out.mapping[key] = value
			out.order = append(out.order, key)
			continue
		}
		// A bare `key:` opens a block or a list, unless nothing is indented
		// under it — in which case it is an empty value, which the schema walk
		// reports against the field rather than here.
		if *pos >= len(lines) || lines[*pos].indent <= indent {
			out.mapping[key] = node{kind: kindScalar, line: cur.number}
			out.order = append(out.order, key)
			continue
		}
		child, err := parseBlock(lines, pos, lines[*pos].indent)
		if err != nil {
			return node{}, err
		}
		out.mapping[key] = child
		out.order = append(out.order, key)
	}
	return out, nil
}

// parseSeq reads a list of scalars. A list of blocks is refused by name: no
// field of this schema takes one, and accepting it would mean the reader
// carries a shape nothing reads.
func parseSeq(lines []line, pos *int, indent int) (node, error) {
	out := node{kind: kindSeq, line: lines[*pos].number}
	for *pos < len(lines) {
		cur := lines[*pos]
		if cur.indent < indent {
			break
		}
		if cur.indent > indent || cur.text[0] != '-' {
			return node{}, fmt.Errorf("line %d: %q is not a `- value` item of the list above it", cur.number, cur.text)
		}
		item := strings.TrimSpace(strings.TrimPrefix(cur.text, "-"))
		if item == "" {
			return node{}, fmt.Errorf("line %d: an empty list item", cur.number)
		}
		if strings.Contains(item, ":") && !strings.Contains(item, "://") {
			// `- key: value` is a list of blocks. Refused by name so the reader
			// is told what this schema takes, rather than being handed a
			// silently ignored item.
			if _, rest, ok := strings.Cut(item, ":"); ok && (rest == "" || strings.HasPrefix(rest, " ")) {
				return node{}, fmt.Errorf("line %d: %q is a list of blocks; every list in this file is a list of values", cur.number, item)
			}
		}
		value, err := scalarNode(item, cur.number)
		if err != nil {
			return node{}, err
		}
		out.seq = append(out.seq, value)
		*pos++
	}
	return out, nil
}

// scalarNode reads one scalar and refuses the constructs this subset does not
// implement. Each refusal names the construct: a reader who wrote an anchor
// gets told anchors are not read, not that their value is malformed.
func scalarNode(raw string, number int) (node, error) {
	switch {
	case strings.HasPrefix(raw, "&") || strings.HasPrefix(raw, "*"):
		return node{}, fmt.Errorf("line %d: anchors and aliases are not read; write the value out", number)
	case strings.HasPrefix(raw, "|") || strings.HasPrefix(raw, ">"):
		return node{}, fmt.Errorf("line %d: block scalars (| and >) are not read; this file holds no multi-line value", number)
	case strings.HasPrefix(raw, "{") || strings.HasPrefix(raw, "["):
		return node{}, fmt.Errorf("line %d: flow style ({…} and […]) is not read; write the block out on its own lines", number)
	case strings.HasPrefix(raw, "!"):
		return node{}, fmt.Errorf("line %d: tags (!…) are not read", number)
	}
	value, err := unquote(raw, number)
	if err != nil {
		return node{}, err
	}
	return node{kind: kindScalar, line: number, scalar: value}, nil
}

// unquote strips one layer of quotes. A double-quoted value goes through
// strconv.Unquote so an escape means what YAML says it means; a single-quoted
// one is literal but for the doubled quote.
func unquote(raw string, number int) (string, error) {
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		out, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("line %d: %s is not a readable double-quoted value: %w", number, raw, err)
		}
		return out, nil
	}
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return strings.ReplaceAll(raw[1:len(raw)-1], "''", "'"), nil
	}
	if strings.HasPrefix(raw, "\"") || strings.HasPrefix(raw, "'") {
		return "", fmt.Errorf("line %d: %s opens a quote it never closes", number, raw)
	}
	return raw, nil
}
