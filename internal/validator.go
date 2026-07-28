package internal

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-playground/locales/en"
	"github.com/go-playground/locales/zh"
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/validator/v10"
	zhTranslations "github.com/go-playground/validator/v10/translations/zh"
	"github.com/labstack/echo/v5"
)

type CustomValidator struct {
	Validator *validator.Validate
	trans     ut.Translator
}

func (cv *CustomValidator) TransInit() error {
	zhT := zh.New() //chinese
	enT := en.New() //english
	uni := ut.New(enT, zhT, enT)

	trans, ok := uni.GetTranslator("zh")
	if !ok {
		return fmt.Errorf("uni.GetTranslator zh failed")
	}
	cv.trans = trans
	//register translate
	// 注册翻译器
	return zhTranslations.RegisterDefaultTranslations(cv.Validator, trans)
}

func (cv *CustomValidator) Validate(i interface{}) error {
	if err := cv.Validator.Struct(i); err != nil {
		var errs validator.ValidationErrors
		ok := errors.As(err, &errs)
		if !ok {
			return err
		}
		translate := errs.Translate(cv.trans)
		var messages []string
		for _, msg := range translate {
			messages = append(messages, msg)
		}
		// Optionally, you could return the error to give each route more control over the status code
		return echo.NewHTTPError(http.StatusBadRequest, strings.Join(messages, ","))
	}
	return nil
}
