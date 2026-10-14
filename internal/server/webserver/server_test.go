package webserver

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
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

func TestIndexHTMLSetsAPIBase(t *testing.T) {
	content, err := fs.Sub(webFS, "chess-client-web")
	if err != nil {
		t.Fatal(err)
	}
	for apiURL, want := range map[string]string{
		"http://localhost:8080":     `<script src="app.js" data-api="http://localhost:8080">`,
		`https://x.test/?a=1&b="2"`: `<script src="app.js" data-api="https://x.test/?a=1&amp;b=&#34;2&#34;">`,
	} {
		page, err := indexHTML(content, apiURL)
		if err != nil {
			t.Fatalf("%s: %v", apiURL, err)
		}
		if n := strings.Count(string(page), want); n != 1 {
			t.Errorf("%s: index.html has %s %d times, want once", apiURL, want, n)
		}
		if strings.Contains(string(page), boardScript) {
			t.Errorf("%s: index.html still loads the board without data-api", apiURL)
		}
	}
}

func TestIndexHTMLRefusesChangedScript(t *testing.T) {
	for name, page := range map[string]string{
		"absent": `<script src="./app.js"></script>`,
		"twice":  `<script src="app.js"></script><script src="app.js"></script>`,
	} {
		content := fstest.MapFS{"index.html": {Data: []byte(page)}}
		if _, err := indexHTML(content, "http://localhost:8080"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
