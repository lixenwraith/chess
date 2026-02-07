package command

import (
	"fmt"
	"strings"
	"time"

	"chess/internal/client/api"
	"chess/internal/client/display"
	"chess/internal/client/session"
)

func (r *Registry) registerDebugCommands() {
	r.Register(&Command{
		Name:        "health",
		ShortName:   ".",
		Description: "Check server health",
		Usage:       "health",
		Handler:     healthHandler,
	})

	r.Register(&Command{
		Name:        "url",
		ShortName:   "/",
		Description: "Set API base URL",
		Usage:       "url [apiUrl]",
		Handler:     urlHandler,
	})

	r.Register(&Command{
		Name:        "raw",
		ShortName:   ":",
		Description: "Send raw API request",
		Usage:       "raw <method> <path> [json-body]",
		Handler:     rawRequestHandler,
	})
}

func healthHandler(s *session.Session, args []string) error {
	c := s.GetClient().(*api.Client)
	resp, err := c.Health()
	if err != nil {
		return err
	}

	display.Println(display.Cyan, "Server Health:")
	fmt.Printf("  Status:  %s\n", resp.Status)
	// Convert Unix timestamp to readable time
	t := time.Unix(resp.Time, 0)
	fmt.Printf("  Time:    %s\n", t.Format("2006-01-02 15:04:05"))
	if resp.Storage != "" {
		fmt.Printf("  Storage: %s\n", resp.Storage)
	}

	return nil
}

func urlHandler(s *session.Session, args []string) error {
	if len(args) == 0 {
		fmt.Printf("Current API URL: %s\n", s.GetAPIBaseURL())
		return nil
	}

	url := args[0]
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}

	s.SetAPIBaseURL(url)
	c := s.GetClient().(*api.Client)
	c.SetBaseURL(url)

	display.Println(display.Cyan, "API URL set to: %s", url)
	return nil
}

func rawRequestHandler(s *session.Session, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: raw <method> <path> [json-body]")
	}

	method := strings.ToUpper(args[0])
	path := args[1]

	body := ""
	if len(args) > 2 {
		body = strings.Join(args[2:], " ")
	}

	c := s.GetClient().(*api.Client)
	return c.RawRequest(method, path, body)
}