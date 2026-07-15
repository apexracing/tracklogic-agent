package agent

import "github.com/apexracing/tracklogic-agent/security"

// Option customizes Harness dependencies without changing the serializable
// Config format.
type Option func(*harnessOptions)

type harnessOptions struct {
	permissionManager security.PermissionManager
	inputValidator    security.InputValidator
	outputValidator   security.OutputValidator
	sanitizer         security.Sanitizer
}

func WithPermissionManager(manager security.PermissionManager) Option {
	return func(options *harnessOptions) { options.permissionManager = manager }
}

func WithInputValidator(validator security.InputValidator) Option {
	return func(options *harnessOptions) { options.inputValidator = validator }
}

func WithOutputValidator(validator security.OutputValidator) Option {
	return func(options *harnessOptions) { options.outputValidator = validator }
}

func WithSanitizer(sanitizer security.Sanitizer) Option {
	return func(options *harnessOptions) { options.sanitizer = sanitizer }
}
