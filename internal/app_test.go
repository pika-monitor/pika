package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestErrorHandler(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		status    int
		code      int
		message   string
		errorLogs int
	}{
		{name: "missing route", status: 404, code: 404, message: "Not Found"},
		{name: "not found", err: echo.ErrNotFound, status: 404, code: 404, message: "Not Found"},
		{name: "wrapped not found", err: fmt.Errorf("asset: %w", error(echo.ErrNotFound)), status: 404, code: 404, message: "asset: Not Found"},
		{name: "method not allowed", err: echo.ErrMethodNotAllowed, status: 405, code: 405, message: "Method Not Allowed"},
		{name: "unauthorized", err: echo.ErrUnauthorized, status: 401, code: 401, message: "Unauthorized"},
		{name: "custom HTTP error", err: echo.NewHTTPError(404, "missing asset"), status: 404, code: 404, message: "code=404, message=missing asset"},
		{name: "business error", err: orz.NewError(1001, "invalid input"), status: 400, code: 1001, message: "invalid input"},
		{name: "business unauthorized", err: orz.NewError(401, "login required"), status: 401, code: 401, message: "login required"},
		{name: "business not found", err: orz.NewError(404, "missing resource"), status: 404, code: 404, message: "missing resource"},
		{name: "business server error", err: orz.NewError(500, "database unavailable"), status: 500, code: 500, message: "Internal Server Error", errorLogs: 1},
		{name: "wrapped business error", err: fmt.Errorf("input: %w", orz.NewError(1001, "invalid input")), status: 400, code: 1001, message: "input: invalid input"},
		{name: "HTTP server error", err: echo.NewHTTPError(500, "database unavailable"), status: 500, code: 500, message: "Internal Server Error", errorLogs: 1},
		{name: "wrapped HTTP server error", err: fmt.Errorf("query: %w", echo.NewHTTPError(503, "backend unavailable")), status: 503, code: 503, message: "Internal Server Error", errorLogs: 1},
		{name: "formatted error", err: fmt.Errorf("query failed: %s", "database unavailable"), status: 500, code: 500, message: "Internal Server Error", errorLogs: 1},
		{name: "unexpected error", err: errors.New("database unavailable"), status: 500, code: 500, message: "Internal Server Error", errorLogs: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			core, logs := observer.New(zap.ErrorLevel)
			e := echo.New()
			e.Use(ErrorHandler(zap.New(core)))
			if test.err != nil {
				e.GET("/test", func(c *echo.Context) error { return test.err })
			}
			recorder := httptest.NewRecorder()
			e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/test", nil))
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.status, recorder.Body.String())
			}
			var body struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != test.code || body.Message != test.message {
				t.Errorf("body = %+v, want code=%d message=%q", body, test.code, test.message)
			}
			if logs.Len() != test.errorLogs {
				t.Errorf("error logs = %d, want %d", logs.Len(), test.errorLogs)
			}
			if test.errorLogs > 0 && logs.Len() == test.errorLogs {
				fields := logs.All()[0].ContextMap()
				if fields["error"] != test.err.Error() || fields["method"] != http.MethodGet || fields["path"] != "/test" || fields["status"] != int64(test.status) {
					t.Errorf("unexpected error log context: %+v", fields)
				}
			}
		})
	}
}
