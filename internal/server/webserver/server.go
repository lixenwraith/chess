package webserver

import (
	"embed"
	"fmt"
	"html"
	"io/fs"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

//go:embed chess-client-web
var webFS embed.FS

// Start initializes and starts the web UI server
func Start(host string, port int, apiURL string, logRequests bool) error {
	app := fiber.New(fiber.Config{
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	})

	// Middleware
	if logRequests {
		app.Use(logger.New(logger.Config{
			Format:     "${time} WEB ${status} ${method} ${path} ${latency}\n",
			TimeFormat: time.RFC3339,
			TimeZone:   "UTC",
		}))
	}
	app.Use(cors.New())

	// Create a sub-filesystem rooted at the embedded web client.
	webContent, err := fs.Sub(webFS, "chess-client-web")
	if err != nil {
		return fmt.Errorf("failed to create web sub-filesystem: %w", err)
	}

	index, err := indexHTML(webContent, apiURL)
	if err != nil {
		return err
	}

	// Serve static files from the embedded client directory.
	app.Get("*", func(c *fiber.Ctx) error {
		// The path for the embedded filesystem must not have a leading slash
		fsPath := strings.TrimPrefix(c.Path(), "/")

		sendIndex := func() error {
			c.Set("Content-Type", "text/html; charset=utf-8")
			return c.Send(index)
		}
		if fsPath == "" || fsPath == "index.html" {
			return sendIndex()
		}

		// Try to read the file
		data, err := fs.ReadFile(webContent, fsPath)
		if err != nil {
			// If the file isn't found, serve index.html for SPA-style routing
			// This handles client-side routes that don't correspond to a file
			return sendIndex()
		}

		// Set the correct Content-Type based on file extension
		contentType := "application/octet-stream"
		switch {
		case strings.HasSuffix(fsPath, ".html"):
			contentType = "text/html; charset=utf-8"
		case strings.HasSuffix(fsPath, ".js"):
			contentType = "application/javascript; charset=utf-8"
		case strings.HasSuffix(fsPath, ".css"):
			contentType = "text/css; charset=utf-8"
		}
		c.Set("Content-Type", contentType)

		return c.Send(data)
	})

	addr := fmt.Sprintf("%s:%d", host, port)
	return app.Listen(addr)
}

// boardScript is how index.html loads the board; indexHTML adds the API base.
const boardScript = `<script src="app.js">`

// indexHTML returns the client's index.html with the browser-visible API base
// on the board's script element, data-api, which app.js reads as it runs: the
// same attribute a host that publishes the files itself sets.
func indexHTML(content fs.FS, apiURL string) ([]byte, error) {
	data, err := fs.ReadFile(content, "index.html")
	if err != nil {
		return nil, fmt.Errorf("embedded index.html: %w", err)
	}
	page := string(data)
	if n := strings.Count(page, boardScript); n != 1 {
		return nil, fmt.Errorf("embedded index.html has %s %d times, want once", boardScript, n)
	}
	tag := fmt.Sprintf(`<script src="app.js" data-api="%s">`, html.EscapeString(apiURL))
	return []byte(strings.Replace(page, boardScript, tag, 1)), nil
}
