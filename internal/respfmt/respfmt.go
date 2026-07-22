// Package respfmt renders RESP replies in pretty (redis-cli-style) or raw
// form. Shared by toykv-cli (writes to stdout/stderr) and toykv-tui
// (renders into the value pane). Lifted from cmd/toykv-cli/print.go in
// M7 so both consumers share one implementation. It renders both the
// RESP2 kinds and the RESP3 kinds (% map, ~ set, > push, , double,
// # boolean, _ null, = verbatim) a HELLO 3 connection returns.
package respfmt

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// Printer writes RESP2 values to Out (and errors to Err). Raw selects
// byte-faithful output for scripts; the default is pretty.
type Printer struct {
	Out io.Writer
	Err io.Writer
	Raw bool
}

// Print writes a top-level reply. Errors land on Err; everything else
// on Out. Arrays nest with redis-cli-style "N) " prefixes.
func (p *Printer) Print(v resp.Value) {
	if v.Kind == resp.KindError {
		fmt.Fprintf(p.Err, "(error) %s\n", v.Str)
		return
	}
	if p.Raw {
		p.printRaw(v)
		return
	}
	p.printPretty(v, "")
}

func (p *Printer) printPretty(v resp.Value, indent string) {
	switch v.Kind {
	case resp.KindSimpleString:
		fmt.Fprintln(p.Out, v.Str)
	case resp.KindInteger:
		fmt.Fprintf(p.Out, "(integer) %d\n", v.Int)
	case resp.KindBulkString:
		if v.IsNull {
			fmt.Fprintln(p.Out, "(nil)")
			return
		}
		fmt.Fprintln(p.Out, strconv.Quote(string(v.Bytes)))
	case resp.KindArray, resp.KindSet, resp.KindPush:
		if v.IsNull {
			fmt.Fprintln(p.Out, "(nil)")
			return
		}
		p.printAggregate(v, indent, ")")
	case resp.KindMap:
		p.printMap(v, indent)
	case resp.KindDouble:
		fmt.Fprintf(p.Out, "(double) %s\n", formatDouble(v.Float))
	case resp.KindBoolean:
		if v.Bool {
			fmt.Fprintln(p.Out, "(true)")
		} else {
			fmt.Fprintln(p.Out, "(false)")
		}
	case resp.KindNull:
		fmt.Fprintln(p.Out, "(nil)")
	case resp.KindVerbatim:
		// A verbatim string carries a text block (e.g. INFO); print it
		// as-is rather than quoting so multi-line content stays readable.
		fmt.Fprintln(p.Out, string(v.Bytes))
	default:
		fmt.Fprintf(p.Out, "(unknown kind %q)\n", v.Kind)
	}
}

// printAggregate renders an array/set/push with redis-cli-style "N<sep> "
// prefixes (sep is ")" for arrays and sets/pushes alike).
func (p *Printer) printAggregate(v resp.Value, indent, sep string) {
	if len(v.Array) == 0 {
		fmt.Fprintln(p.Out, "(empty array)")
		return
	}
	width := len(strconv.Itoa(len(v.Array)))
	for i, el := range v.Array {
		prefix := fmt.Sprintf("%s%*d%s ", indent, width, i+1, sep)
		fmt.Fprint(p.Out, prefix)
		if isNonEmptyAggregate(el) {
			fmt.Fprintln(p.Out)
			p.printPretty(el, indent+strings.Repeat(" ", len(prefix)))
			continue
		}
		p.printPretty(el, "")
	}
}

// printMap renders a RESP3 map as redis-cli does: "N# key => value".
func (p *Printer) printMap(v resp.Value, indent string) {
	if len(v.Array) == 0 {
		fmt.Fprintln(p.Out, "(empty map)")
		return
	}
	pairs := len(v.Array) / 2
	width := len(strconv.Itoa(pairs))
	for i := 0; i < pairs; i++ {
		key, val := v.Array[2*i], v.Array[2*i+1]
		prefix := fmt.Sprintf("%s%*d# ", indent, width, i+1)
		fmt.Fprintf(p.Out, "%s%s => ", prefix, leafString(key))
		if isNonEmptyAggregate(val) {
			fmt.Fprintln(p.Out)
			p.printPretty(val, indent+strings.Repeat(" ", len(prefix)))
			continue
		}
		p.printPretty(val, "")
	}
}

func (p *Printer) printRaw(v resp.Value) {
	switch v.Kind {
	case resp.KindSimpleString:
		fmt.Fprintln(p.Out, v.Str)
	case resp.KindInteger:
		fmt.Fprintln(p.Out, v.Int)
	case resp.KindBulkString, resp.KindVerbatim:
		if v.IsNull {
			fmt.Fprintln(p.Out)
			return
		}
		_, _ = p.Out.Write(v.Bytes)
		fmt.Fprintln(p.Out)
	case resp.KindArray, resp.KindSet, resp.KindPush, resp.KindMap:
		if v.IsNull {
			fmt.Fprintln(p.Out)
			return
		}
		for _, el := range v.Array {
			p.printRaw(el)
		}
	case resp.KindDouble:
		fmt.Fprintln(p.Out, formatDouble(v.Float))
	case resp.KindBoolean:
		if v.Bool {
			fmt.Fprintln(p.Out, "true")
		} else {
			fmt.Fprintln(p.Out, "false")
		}
	case resp.KindNull:
		fmt.Fprintln(p.Out)
	}
}

// leafString renders a scalar reply inline (no trailing newline) for use
// as a map key. Aggregates fall back to a bracketed kind hint — maps with
// aggregate keys are not produced by any current command.
func leafString(v resp.Value) string {
	switch v.Kind {
	case resp.KindSimpleString:
		return v.Str
	case resp.KindInteger:
		return fmt.Sprintf("(integer) %d", v.Int)
	case resp.KindBulkString:
		if v.IsNull {
			return "(nil)"
		}
		return strconv.Quote(string(v.Bytes))
	case resp.KindDouble:
		return "(double) " + formatDouble(v.Float)
	case resp.KindBoolean:
		if v.Bool {
			return "(true)"
		}
		return "(false)"
	case resp.KindNull:
		return "(nil)"
	case resp.KindVerbatim:
		return strconv.Quote(string(v.Bytes))
	default:
		return fmt.Sprintf("(%c)", byte(v.Kind))
	}
}

// isNonEmptyAggregate reports whether v is a non-nil array/set/push/map
// with at least one element — the case that warrants a nested, indented
// render rather than an inline one.
func isNonEmptyAggregate(v resp.Value) bool {
	switch v.Kind {
	case resp.KindArray, resp.KindSet, resp.KindPush, resp.KindMap:
		return !v.IsNull && len(v.Array) > 0
	}
	return false
}

// formatDouble renders a RESP3 double for display, mirroring the wire
// forms the server emits: inf / -inf / nan and the shortest decimal.
func formatDouble(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	default:
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
}

// PrettyString renders v as a redis-cli-style string. Convenience for
// callers (e.g. the TUI value pane) that want a string rather than a
// writer.
func PrettyString(v resp.Value) string {
	var sb strings.Builder
	p := &Printer{Out: &sb, Err: &sb}
	p.Print(v)
	return strings.TrimRight(sb.String(), "\n")
}

// RawString renders v in raw mode as a string. Errors render with the
// "(error) " prefix to match Printer's stderr behaviour.
func RawString(v resp.Value) string {
	var sb strings.Builder
	p := &Printer{Out: &sb, Err: &sb, Raw: true}
	p.Print(v)
	return strings.TrimRight(sb.String(), "\n")
}
