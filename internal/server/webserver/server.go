// FILE: lixenwraith/chess/internal/server/webserver/server.go
package webserver

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

//go:embed web
var webFS embed.FS

// Start initializes and starts the web UI server
func Start(host string, port int, apiURL string) error {
	app := fiber.New(fiber.Config{
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	})

	// Middleware
	app.Use(logger.New(logger.Config{
		Format: "${time} WEB ${status} ${method} ${path} ${latency}\n",
	}))
	app.Use(cors.New())

	// Create a sub-filesystem that points to the 'web' directory
	webContent, err := fs.Sub(webFS, "web")
	if err != nil {
		return fmt.Errorf("failed to create web sub-filesystem: %w", err)
	}

	// API config endpoint, served before the static file handler
	app.Get("/config", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"apiUrl": apiURL,
		})
	})

	// Serve static files from the embedded 'web' directory
	app.Get("*", func(c *fiber.Ctx) error {
		path := c.Path()

		// Default to index.html for the root path
		if path == "/" {
			path = "/index.html"
		}

		// The path for the embedded filesystem must not have a leading slash
		fsPath := strings.TrimPrefix(path, "/")

		// Try to read the file
		data, err := fs.ReadFile(webContent, fsPath)
		if err != nil {
			// If the file isn't found, serve index.html for SPA-style routing.
			// This handles client-side routes that don't correspond to a file.
			data, err = fs.ReadFile(webContent, "index.html")
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).SendString("index.html not found")
			}
			c.Set("Content-Type", "text/html; charset=utf-8")
			return c.Send(data)
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