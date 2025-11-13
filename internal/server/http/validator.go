// FILE: lixenwraith/chess/internal/server/http/handler.go
package http

import (
	"chess/internal/server/core"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// Add validator instance near top of file
var validate = validator.New()

// Add custom validation middleware function
func validationMiddleware(c *fiber.Ctx) error {
	// Skip validation for GET, DELETE, OPTIONS
	method := c.Method()
	if method == fiber.MethodGet || method == fiber.MethodDelete || method == fiber.MethodOptions {
		return c.Next()
	}

	// Determine request type based on path
	path := c.Path()
	var requestType interface{}

	switch {
	case strings.HasSuffix(path, "/games") && method == fiber.MethodPost:
		requestType = &core.CreateGameRequest{}
	case strings.HasSuffix(path, "/players") && method == fiber.MethodPut:
		requestType = &core.ConfigurePlayersRequest{}
	case strings.HasSuffix(path, "/moves") && method == fiber.MethodPost:
		requestType = &core.MoveRequest{}
	case strings.HasSuffix(path, "/undo") && method == fiber.MethodPost:
		requestType = &core.UndoRequest{}
	default:
		return c.Next() // No validation for unknown endpoints
	}

	// Parse body
	if err := c.BodyParser(requestType); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "invalid request body",
			Code:    core.ErrInvalidRequest,
			Details: err.Error(),
		})
	}

	// Validate
	if errs := validate.Struct(requestType); errs != nil {
		var details strings.Builder
		for _, err := range errs.(validator.ValidationErrors) {
			if details.Len() > 0 {
				details.WriteString("; ")
			}
			switch err.Tag() {
			case "required":
				details.WriteString(fmt.Sprintf("%s is required", err.Field()))
			case "oneof":
				details.WriteString(fmt.Sprintf("%s must be one of [%s]", err.Field(), err.Param()))
			case "min":
				if err.Type().Kind() == reflect.String {
					details.WriteString(fmt.Sprintf("%s must be at least %s characters", err.Field(), err.Param()))
				} else {
					details.WriteString(fmt.Sprintf("%s must be at least %s", err.Field(), err.Param()))
				}
			case "max":
				if err.Type().Kind() == reflect.String {
					details.WriteString(fmt.Sprintf("%s must be at most %s characters", err.Field(), err.Param()))
				} else {
					details.WriteString(fmt.Sprintf("%s must be at most %s", err.Field(), err.Param()))
				}
			case "omitempty": // Skip, a control tag that doesn't error
				continue
			case "dive": // Skip, panics on wrong type, no error handling since current code does not call validator on slice or map
				continue
			default:
				details.WriteString(fmt.Sprintf("%s failed %s validation", err.Field(), err.Tag()))
			}
		}

		return c.Status(fiber.StatusBadRequest).JSON(core.ErrorResponse{
			Error:   "validation failed",
			Code:    core.ErrInvalidRequest,
			Details: details.String(),
		})
	}

	// Store validated body for handler use
	c.Locals("validatedBody", requestType)
	c.Locals("validated", true)

	return c.Next()
}

func isValidUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}