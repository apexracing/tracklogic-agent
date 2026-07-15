package agent

import (
	"log/slog"

	"github.com/apexracing/tracklogic-agent/mcp"
	"github.com/apexracing/tracklogic-agent/model"
	"github.com/apexracing/tracklogic-agent/security"
)

// Option customizes Harness dependencies without changing the serializable
// Config format.
type Option func(*harnessOptions)

type harnessOptions struct {
	permissionManager security.PermissionManager
	inputValidator    security.InputValidator
	outputValidator   security.OutputValidator
	sanitizer         security.Sanitizer
	logger            *slog.Logger
	mcpClients        map[string]*mcp.Client
	model             model.Model
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

// WithLogger injects the application logger without changing slog.Default.
func WithLogger(logger *slog.Logger) Option {
	return func(options *harnessOptions) { options.logger = logger }
}

// WithModel injects a preconfigured default Model. This is useful for custom
// HTTP transports, provider wrappers, tests, and application-owned retry
// policies. The serializable model configuration is still validated.
func WithModel(runtimeModel model.Model) Option {
	return func(options *harnessOptions) { options.model = runtimeModel }
}

// WithMCPClient injects a preconfigured public MCP client. It is useful when
// authentication, custom TLS, proxies, or another HTTP transport must be
// configured programmatically instead of stored in JSON. An injected client
// replaces a configured client with the same name.
func WithMCPClient(name string, client *mcp.Client) Option {
	return func(options *harnessOptions) {
		if options.mcpClients == nil {
			options.mcpClients = make(map[string]*mcp.Client)
		}
		options.mcpClients[name] = client
	}
}
