package webserver

import (
	"io/fs"
	"testing"
)

func TestEmbeddedWebClientRoot(t *testing.T) {
	content, err := fs.Sub(webFS, "chess-client-web")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "app.js", "style.css"} {
		data, err := fs.ReadFile(content, name)
		if err != nil {
			t.Errorf("read embedded %s: %v", name, err)
		}
		if len(data) == 0 {
			t.Errorf("embedded %s is empty", name)
		}
	}
}
