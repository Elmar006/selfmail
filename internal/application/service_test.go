package application

import (
	"strings"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestTemplateEscapingAndMissingVariables(t *testing.T) {
	t.Run("escaped", func(t *testing.T) {
		r := domain.SendRequest{}
		e := Render(domain.Template{Subject: "Hello {{.Name}}", HTML: "<p>{{.Name}}</p>"}, map[string]any{"Name": "<script>alert(1)</script>"}, &r)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(r.HTML, "<script>") || !strings.Contains(r.HTML, "&lt;script&gt;") {
			t.Fatal("HTML injection")
		}
	})
	t.Run("missing", func(t *testing.T) {
		if Render(domain.Template{Text: "{{.Missing}}"}, nil, &domain.SendRequest{}) == nil {
			t.Fatal("missing variable silently accepted")
		}
	})
}
