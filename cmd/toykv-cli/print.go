package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// printer renders RESP2 replies in either pretty (redis-cli-style) or
// raw form. Errors always go to err; OK replies go to out.
type printer struct {
	out io.Writer
	err io.Writer
	raw bool
}

// print writes a top-level reply. Errors land on stderr; everything
// else on stdout. Arrays nest with redis-cli-style "N) " prefixes.
func (p *printer) print(v resp.Value) {
	if v.Kind == resp.KindError {
		fmt.Fprintf(p.err, "(error) %s\n", v.Str)
		return
	}
	if p.raw {
		p.printRaw(v)
		return
	}
	p.printPretty(v, "")
}

func (p *printer) printPretty(v resp.Value, indent string) {
	switch v.Kind {
	case resp.KindSimpleString:
		fmt.Fprintln(p.out, v.Str)
	case resp.KindInteger:
		fmt.Fprintf(p.out, "(integer) %d\n", v.Int)
	case resp.KindBulkString:
		if v.IsNull {
			fmt.Fprintln(p.out, "(nil)")
			return
		}
		fmt.Fprintln(p.out, strconv.Quote(string(v.Bytes)))
	case resp.KindArray:
		if v.IsNull {
			fmt.Fprintln(p.out, "(nil)")
			return
		}
		if len(v.Array) == 0 {
			fmt.Fprintln(p.out, "(empty array)")
			return
		}
		width := len(strconv.Itoa(len(v.Array)))
		for i, el := range v.Array {
			prefix := fmt.Sprintf("%s%*d) ", indent, width, i+1)
			fmt.Fprint(p.out, prefix)
			if el.Kind == resp.KindArray && !el.IsNull && len(el.Array) > 0 {
				// Drop into nested rendering on the next line for clarity.
				fmt.Fprintln(p.out)
				p.printPretty(el, indent+strings.Repeat(" ", len(prefix)))
				continue
			}
			p.printPretty(el, "")
		}
	default:
		fmt.Fprintf(p.out, "(unknown kind %q)\n", v.Kind)
	}
}

// printRaw writes byte-faithful output for scripting.
func (p *printer) printRaw(v resp.Value) {
	switch v.Kind {
	case resp.KindSimpleString:
		fmt.Fprintln(p.out, v.Str)
	case resp.KindInteger:
		fmt.Fprintln(p.out, v.Int)
	case resp.KindBulkString:
		if v.IsNull {
			// Empty line per redis-cli convention for nil in raw mode.
			fmt.Fprintln(p.out)
			return
		}
		_, _ = p.out.Write(v.Bytes)
		fmt.Fprintln(p.out)
	case resp.KindArray:
		if v.IsNull {
			fmt.Fprintln(p.out)
			return
		}
		for _, el := range v.Array {
			p.printRaw(el)
		}
	}
}
