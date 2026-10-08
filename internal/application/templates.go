package application

import (
	"context"
	"encoding/json"
	"fmt"
	htmltpl "html/template"
	"strings"
	texttpl "text/template"
	"text/template/parse"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
)

const renderTimeout = 500 * time.Millisecond

type renderBudget struct {
	ctx                    context.Context
	operations, iterations int
}

func (b *renderBudget) step() (string, error) {
	if e := b.ctx.Err(); e != nil {
		return "", e
	}
	b.operations++
	if b.operations > 50000 {
		return "", fmt.Errorf("template operation budget exceeded")
	}
	return "", nil
}
func (b *renderBudget) loop() (string, error) {
	if _, e := b.step(); e != nil {
		return "", e
	}
	b.iterations++
	if b.iterations > 10000 {
		return "", fmt.Errorf("template iteration budget exceeded")
	}
	return "", nil
}
func (b *renderBudget) funcs() texttpl.FuncMap {
	return texttpl.FuncMap{"__selfmail_step": b.step, "__selfmail_loop": b.loop}
}

// Only JSON data enters templates; methods and function values cannot execute.
func boundedVariables(ctx context.Context, vars map[string]any) (map[string]any, error) {
	nodes, size := 0, 0
	var visit func(any, int) (any, error)
	visit = func(v any, depth int) (any, error) {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		nodes++
		if nodes > 10000 || depth > 16 {
			return nil, fmt.Errorf("variables nesting/node budget exceeded")
		}
		size += 16
		if size > 256*1024 {
			return nil, fmt.Errorf("variables byte budget exceeded")
		}
		switch x := v.(type) {
		case nil, bool, float64, float32, int, int64, int32, uint, uint64:
			return x, nil
		case json.Number:
			size += len(x)
			if len(x) > 128 || size > 256*1024 {
				return nil, fmt.Errorf("JSON numeric budget exceeded")
			}
			if _, e := x.Float64(); e != nil {
				return nil, fmt.Errorf("invalid JSON number")
			}
			return x, nil
		case string:
			size += len(x)
			if size > 256*1024 {
				return nil, fmt.Errorf("variables byte budget exceeded")
			}
			return x, nil
		case []any:
			if len(x) > 10000-nodes {
				return nil, fmt.Errorf("variables node budget exceeded")
			}
			out := make([]any, len(x))
			for i, e := range x {
				n, err := visit(e, depth+1)
				if err != nil {
					return nil, err
				}
				out[i] = n
			}
			return out, nil
		case map[string]any:
			if len(x) > 10000-nodes {
				return nil, fmt.Errorf("variables node budget exceeded")
			}
			out := make(map[string]any, len(x))
			for k, e := range x {
				size += len(k)
				n, err := visit(e, depth+1)
				if err != nil {
					return nil, err
				}
				out[k] = n
			}
			return out, nil
		default:
			return nil, fmt.Errorf("variables must contain JSON values")
		}
	}
	if vars == nil {
		return nil, nil
	}
	v, e := visit(vars, 0)
	if e != nil {
		return nil, e
	}
	return v.(map[string]any), nil
}

var safeTemplateFunctions = map[string]bool{"and": true, "or": true, "not": true, "len": true, "index": true, "slice": true, "eq": true, "ne": true, "lt": true, "le": true, "gt": true, "ge": true}

func validatePipe(p *parse.PipeNode, depth int) error {
	if p == nil {
		return nil
	}
	if depth > 16 {
		return fmt.Errorf("template pipeline nesting exceeded")
	}
	for _, cmd := range p.Cmds {
		if len(cmd.Args) > 16 {
			return fmt.Errorf("too many function arguments")
		}
		for _, a := range cmd.Args {
			switch n := a.(type) {
			case *parse.IdentifierNode:
				if !safeTemplateFunctions[n.Ident] {
					return fmt.Errorf("unsupported template function %s", n.Ident)
				}
			case *parse.PipeNode:
				if e := validatePipe(n, depth+1); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
func prepareTrees(trees map[string]*parse.Tree, b *renderBudget, meter bool) error {
	edges := map[string][]string{}
	var check func(*parse.ListNode, int, string) error
	check = func(l *parse.ListNode, d int, name string) error {
		if l == nil {
			return nil
		}
		if d > 16 {
			return fmt.Errorf("template control nesting exceeded")
		}
		for _, node := range l.Nodes {
			var p *parse.PipeNode
			switch n := node.(type) {
			case *parse.ActionNode:
				p = n.Pipe
			case *parse.IfNode:
				p = n.Pipe
				if e := check(n.List, d+1, name); e != nil {
					return e
				}
				if e := check(n.ElseList, d+1, name); e != nil {
					return e
				}
			case *parse.WithNode:
				p = n.Pipe
				if e := check(n.List, d+1, name); e != nil {
					return e
				}
				if e := check(n.ElseList, d+1, name); e != nil {
					return e
				}
			case *parse.RangeNode:
				p = n.Pipe
				if e := check(n.List, d+1, name); e != nil {
					return e
				}
				if e := check(n.ElseList, d+1, name); e != nil {
					return e
				}
			case *parse.TemplateNode:
				p = n.Pipe
				edges[name] = append(edges[name], n.Name)
			}
			if e := validatePipe(p, 0); e != nil {
				return e
			}
		}
		return nil
	}
	for name, t := range trees {
		if e := check(t.Root, 0, name); e != nil {
			return e
		}
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var walk func(string, int) error
	walk = func(name string, d int) error {
		if visiting[name] || d > 16 {
			return fmt.Errorf("recursive/deep template invocation prohibited")
		}
		if done[name] {
			return nil
		}
		if trees[name] == nil {
			return fmt.Errorf("undefined template %s", name)
		}
		visiting[name] = true
		for _, next := range edges[name] {
			if e := walk(next, d+1); e != nil {
				return e
			}
		}
		visiting[name] = false
		done[name] = true
		return nil
	}
	for name := range trees {
		if e := walk(name, 0); e != nil {
			return e
		}
	}
	if !meter {
		return nil
	}
	// Declarations execute the callback without emitting bytes, even in JS/URL
	// HTML contexts. An output action here would alter contextual escaping.
	step, _ := texttpl.New("meter").Funcs(b.funcs()).Parse("{{$__selfmail_budget := __selfmail_step}}")
	loop, _ := texttpl.New("meter").Funcs(b.funcs()).Parse("{{$__selfmail_budget := __selfmail_loop}}")
	var instrument func(*parse.ListNode)
	instrument = func(l *parse.ListNode) {
		if l == nil {
			return
		}
		out := make([]parse.Node, 0, len(l.Nodes)*2)
		for _, node := range l.Nodes {
			if node.Type() != parse.NodeText {
				out = append(out, step.Tree.Root.Nodes[0].Copy())
			}
			switch n := node.(type) {
			case *parse.IfNode:
				instrument(n.List)
				instrument(n.ElseList)
			case *parse.WithNode:
				instrument(n.List)
				instrument(n.ElseList)
			case *parse.RangeNode:
				instrument(n.List)
				instrument(n.ElseList)
				if n.List != nil {
					n.List.Nodes = append([]parse.Node{loop.Tree.Root.Nodes[0].Copy()}, n.List.Nodes...)
				}
			}
			out = append(out, node)
		}
		l.Nodes = out
	}
	for _, t := range trees {
		instrument(t.Root)
	}
	return nil
}
func CheckTemplateSource(src string, html bool) error {
	if len(src) > 128*1024 || strings.Contains(src, "__selfmail_") {
		return fmt.Errorf("template source/reserved identifier limit")
	}
	b := &renderBudget{ctx: context.Background()}
	trees := map[string]*parse.Tree{}
	if html {
		x, e := htmltpl.New("mail").Funcs(htmltpl.FuncMap(b.funcs())).Parse(src)
		if e != nil {
			return e
		}
		for _, t := range x.Templates() {
			trees[t.Name()] = t.Tree
		}
	} else {
		x, e := texttpl.New("mail").Funcs(b.funcs()).Parse(src)
		if e != nil {
			return e
		}
		for _, t := range x.Templates() {
			trees[t.Name()] = t.Tree
		}
	}
	return prepareTrees(trees, b, false)
}
func RenderContext(parent context.Context, t domain.Template, vars map[string]any, r *domain.SendRequest) error {
	ctx, cancel := context.WithTimeout(parent, renderTimeout)
	defer cancel()
	values, e := boundedVariables(ctx, vars)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return domain.Invalid("variables", e.Error())
	}
	b := &renderBudget{ctx: ctx}
	render := func(src string, html bool, limit int) (string, error) {
		if e := CheckTemplateSource(src, html); e != nil {
			return "", e
		}
		out := boundedBuffer{limit: limit, ctx: ctx}
		trees := map[string]*parse.Tree{}
		if html {
			x, e := htmltpl.New("mail").Funcs(htmltpl.FuncMap(b.funcs())).Option("missingkey=error").Parse(src)
			if e != nil {
				return "", e
			}
			for _, t := range x.Templates() {
				trees[t.Name()] = t.Tree
			}
			if e = prepareTrees(trees, b, true); e != nil {
				return "", e
			}
			e = x.Execute(&out, values)
			return out.String(), e
		}
		x, e := texttpl.New("mail").Funcs(b.funcs()).Option("missingkey=error").Parse(src)
		if e != nil {
			return "", e
		}
		for _, t := range x.Templates() {
			trees[t.Name()] = t.Tree
		}
		if e = prepareTrees(trees, b, true); e != nil {
			return "", e
		}
		e = x.Execute(&out, values)
		return out.String(), e
	}
	r.Subject, e = render(t.Subject, false, 998)
	if e == nil {
		r.Text, e = render(t.Text, false, 5*1024*1024)
	}
	if e == nil {
		r.HTML, e = render(t.HTML, true, 5*1024*1024)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e != nil {
		return domain.Invalid("variables", e.Error())
	}
	return nil
}
