package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestMeteredEmptyRangesAndCancellation(t *testing.T) {
	items := make([]any, 1000)
	for i := range items {
		items[i] = i
	}
	start := time.Now()
	var r domain.SendRequest
	e := RenderContext(context.Background(), domain.Template{Text: "{{range .Items}}{{range $.Items}}{{end}}{{end}}"}, map[string]any{"Items": items}, &r)
	if e == nil || !IsValidation(e) || time.Since(start) > time.Second {
		t.Fatalf("unbounded render: %v %s", e, time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = RenderContext(ctx, domain.Template{Text: "hello"}, nil, &r); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancellation: %v", e)
	}
}
func TestMeterDoesNotEmitHTMLContextBytes(t *testing.T) {
	var r domain.SendRequest
	e := RenderContext(context.Background(), domain.Template{HTML: `<a href="{{.URL}}">{{.Name}}</a><script>var x={{.Value}};</script>`}, map[string]any{"URL": "https://example.test/?a=b&c=d", "Name": "<b>x</b>", "Value": "hello"}, &r)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(r.HTML, "&lt;b&gt;x&lt;/b&gt;") || !strings.Contains(r.HTML, `var x="hello";`) {
		t.Fatal(r.HTML)
	}
}
func TestTemplateRejectsRecursionAndUnboundedFunctions(t *testing.T) {
	for _, s := range []string{`{{define "a"}}{{template "a" .}}{{end}}{{template "a" .}}`, `{{printf "%999999999s" .Name}}`, `{{$__selfmail_budget := .Name}}`} {
		if CheckTemplateSource(s, false) == nil {
			t.Errorf("unsafe template accepted: %s", s)
		}
	}
}
