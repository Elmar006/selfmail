package application

import (
	"strings"
	"testing"

	"github.com/Elmar006/selfmail/internal/domain"
)

func TestTemplateAmplificationIsBounded(t *testing.T) {
	if e := Render(domain.Template{Text: "{{.X}}{{.X}}"}, map[string]any{"X": strings.Repeat("x", 3*1024*1024)}, &domain.SendRequest{}); e == nil {
		t.Fatal("oversized template output accepted")
	}
}
