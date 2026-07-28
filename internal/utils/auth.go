package utils

import "github.com/labstack/echo/v5"

// IsAuthenticated 检查用户是否已登录
func IsAuthenticated(c *echo.Context) bool {
	authenticated, _ := c.Get("authenticated").(bool)
	return authenticated
}
