package handler

import (
	"net/http"

	"github.com/dushixiang/pika/internal/service"
	"github.com/go-orz/orz"
	"github.com/labstack/echo/v5"
)

type AccountHandler struct {
	accountService *service.AccountService
}

func NewAccountHandler(accountService *service.AccountService) *AccountHandler {
	return &AccountHandler{
		accountService: accountService,
	}
}

// LoginRequest 登录请求
type LoginRequest struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
}

// Login 用户登录（Basic Auth）
func (r AccountHandler) Login(c *echo.Context) error {
	var req LoginRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}

	ctx := c.Request().Context()
	loginResp, err := r.accountService.Login(ctx, req.Username, req.Password)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "用户名或密码错误")
	}

	return orz.Ok(c, loginResp)
}

// OIDCLoginRequest OIDC 登录请求
type OIDCLoginRequest struct {
	Code  string `json:"code" validate:"required"`
	State string `json:"state" validate:"required"`
}

// OIDCLogin OIDC 登录回调
func (r AccountHandler) OIDCLogin(c *echo.Context) error {
	var req OIDCLoginRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}

	ctx := c.Request().Context()
	loginResp, err := r.accountService.LoginWithOIDC(ctx, req.Code, req.State)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "OIDC 认证失败: "+err.Error())
	}

	return orz.Ok(c, loginResp)
}

// GetAuthConfig 获取认证配置
func (r AccountHandler) GetAuthConfig(c *echo.Context) error {
	config := r.accountService.GetAuthConfig()
	return orz.Ok(c, config)
}

// GetOIDCAuthURL 获取 OIDC 认证 URL
func (r AccountHandler) GetOIDCAuthURL(c *echo.Context) error {
	authURL, err := r.accountService.GetOIDCAuthURL()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return orz.Ok(c, authURL)
}

// GetGitHubAuthURL 获取 GitHub 认证 URL
func (r AccountHandler) GetGitHubAuthURL(c *echo.Context) error {
	authURL, err := r.accountService.GetGitHubAuthURL()
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return orz.Ok(c, authURL)
}

// GitHubLoginRequest GitHub 登录请求
type GitHubLoginRequest struct {
	Code  string `json:"code" validate:"required"`
	State string `json:"state" validate:"required"`
}

// GitHubLogin GitHub 登录回调
func (r AccountHandler) GitHubLogin(c *echo.Context) error {
	var req GitHubLoginRequest
	if err := c.Bind(&req); err != nil {
		return err
	}
	if err := c.Validate(&req); err != nil {
		return err
	}

	ctx := c.Request().Context()
	loginResp, err := r.accountService.LoginWithGitHub(ctx, req.Code, req.State)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "GitHub 认证失败: "+err.Error())
	}

	return orz.Ok(c, loginResp)
}

// Logout 用户登出
func (r AccountHandler) Logout(c *echo.Context) error {
	userID := c.Get("userID")
	if userID == nil {
		return orz.NewError(401, "未登录")
	}

	ctx := c.Request().Context()
	if err := r.accountService.Logout(ctx, userID.(string)); err != nil {
		return err
	}

	return orz.Ok(c, orz.Map{})
}

// ValidateToken 验证 token（供中间件使用）
func (r AccountHandler) ValidateToken(tokenString string) (*service.JWTClaims, error) {
	return r.accountService.ValidateToken(tokenString)
}

// GetCurrentUser 获取当前登录用户信息
func (r AccountHandler) GetCurrentUser(c *echo.Context) error {
	// 从 context 中获取用户信息（由 JWT 中间件设置）
	userID := c.Get("userID")
	username := c.Get("username")

	if userID == nil || username == nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "未登录")
	}

	return orz.Ok(c, orz.Map{
		"userId":   userID.(string),
		"username": username.(string),
	})
}
