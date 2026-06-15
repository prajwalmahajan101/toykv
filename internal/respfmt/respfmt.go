// Package respfmt renders RESP2 replies in pretty (redis-cli-style) or
// raw form. Shared by toykv-cli (writes to stdout/stderr) and toykv-tui
// (renders into the value pane). Lifted from cmd/toykv-cli/print.go in
// M7 so both consumers share one implementation.
package respfmt

import (
	"fmt"
	"io"
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
	case resp.KindArray:
		if v.IsNull {
			fmt.Fprintln(p.Out, "(nil)")
			return
		}
		if len(v.Array) == 0 {
			fmt.Fprintln(p.Out, "(empty array)")
			return
		}
		width := len(strconv.Itoa(len(v.Array)))
		for i, el := range v.Array {
			prefix := fmt.Sprintf("%s%*d) ", indent, width, i+1)
			fmt.Fprint(p.Out, prefix)
			if el.Kind == resp.KindArray && !el.IsNull && len(el.Array) > 0 {
				fmt.Fprintln(p.Out)
				p.printPretty(el, indent+strings.Repeat(" ", len(prefix)))
				continue
			}
			p.printPretty(el, "")
		}
	default:
		fmt.Fprintf(p.Out, "(unknown kind %q)\n", v.Kind)
	}
}

func (p *Printer) printRaw(v resp.Value) {
	switch v.Kind {
	case resp.KindSimpleString:
		fmt.Fprintln(p.Out, v.Str)
	case resp.KindInteger:
		fmt.Fprintln(p.Out, v.Int)
	case resp.KindBulkString:
		if v.IsNull {
			fmt.Fprintln(p.Out)
			return
		}
		_, _ = p.Out.Write(v.Bytes)
		fmt.Fprintln(p.Out)
	case resp.KindArray:
		if v.IsNull {
			fmt.Fprintln(p.Out)
			return
		}
		for _, el := range v.Array {
			p.printRaw(el)
		}
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
