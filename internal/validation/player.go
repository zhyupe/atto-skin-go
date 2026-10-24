package validation

import (
	"regexp"

	"github.com/go-playground/validator/v10"
)

var names = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
var validate = newValidator() //nolint:gochecknoglobals // Registration completes before concurrent validation.

func newValidator() *validator.Validate {
	v := validator.New()
	if err := v.RegisterValidation(
		"minecraft_name",
		func(f validator.FieldLevel) bool { return names.MatchString(f.Field().String()) },
	); err != nil {
		panic(err)
	}
	return v
}

func PlayerName(name string) bool {
	return validate.Var(name, "required,min=3,max=16,minecraft_name") == nil
}
