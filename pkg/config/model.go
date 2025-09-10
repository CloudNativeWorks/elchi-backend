package config

type AppConfig struct {
	ElchiAddress               string   `mapstructure:"ELCHI_ADDRESS" yaml:"ELCHI_ADDRESS"`
	ElchiPort                  string   `mapstructure:"ELCHI_PORT" yaml:"ELCHI_PORT"`
	ElchiTLSEnabled            string   `mapstructure:"ELCHI_TLS_ENABLED" yaml:"ELCHI_TLS_ENABLED"`
	ElchiEnableDemo            string   `mapstructure:"ELCHI_ENABLE_DEMO" yaml:"ELCHI_ENABLE_DEMO"`
	ElchiVersions              []string `mapstructure:"ELCHI_VERSIONS" yaml:"ELCHI_VERSIONS"`
	ElchiInternalCommunication string   `mapstructure:"ELCHI_INTERNAL_COMMUNICATION" yaml:"ELCHI_INTERNAL_COMMUNICATION"`
	ElchiInternalAddressPort   string   `mapstructure:"ELCHI_INTERNAL_ADDRESS_PORT" yaml:"ELCHI_INTERNAL_ADDRESS_PORT"`
	ElchiNamespace             string   `mapstructure:"ELCHI_NAMESPACE" yaml:"ELCHI_NAMESPACE"`

	MongodbHosts      string `mapstructure:"MONGODB_HOSTS" yaml:"MONGODB_HOSTS"`
	MongodbUsername   string `mapstructure:"MONGODB_USERNAME" yaml:"MONGODB_USERNAME"`
	MongodbPassword   string `mapstructure:"MONGODB_PASSWORD" yaml:"MONGODB_PASSWORD"`
	MongodbPort       string `mapstructure:"MONGODB_PORT" yaml:"MONGODB_PORT"`
	MongodbDatabase   string `mapstructure:"MONGODB_DATABASE" yaml:"MONGODB_DATABASE"`
	MongodbScheme     string `mapstructure:"MONGODB_SCHEME" yaml:"MONGODB_SCHEME"`
	MongodbReplicaSet string `mapstructure:"MONGODB_REPLICASET" yaml:"MONGODB_REPLICASET"`
	MongodbTimeoutMs  string `mapstructure:"MONGODB_TIMEOUTMS" yaml:"MONGODB_TIMEOUTMS"`
	MongodbTLSEnabled string `mapstructure:"MONGODB_TLS_ENABLED" yaml:"MONGODB_TLS_ENABLED"`

	MongodbAuthSource    string `mapstructure:"MONGODB_AUTH_SOURCE" yaml:"MONGODB_AUTH_SOURCE"`
	MongodbAuthMechanism string `mapstructure:"MONGODB_AUTH_MECHANISM" yaml:"MONGODB_AUTH_MECHANISM"`

	// Logging configuration - flattened for environment variable support
	LoggingLevel      string `mapstructure:"LOGGING_LEVEL" yaml:"LOGGING_LEVEL"`
	LoggingFormat     string `mapstructure:"LOGGING_FORMAT" yaml:"LOGGING_FORMAT"`
	LoggingOutputPath string `mapstructure:"LOGGING_OUTPUT_PATH" yaml:"LOGGING_OUTPUT_PATH"`

	// Keep nested for YAML compatibility
	Logging LoggingConfig `mapstructure:"LOGGING" yaml:"LOGGING"`

	SMTPPassword string `mapstructure:"SMTP_PASSWORD" yaml:"SMTP_PASSWORD"`

	// JWT Security configuration
	ElchiJWTSecret               string `mapstructure:"ELCHI_JWT_SECRET" yaml:"ELCHI_JWT_SECRET"`
	ElchiJWTAccessTokenDuration  string `mapstructure:"ELCHI_JWT_ACCESS_TOKEN_DURATION" yaml:"ELCHI_JWT_ACCESS_TOKEN_DURATION"`
	ElchiJWTRefreshTokenDuration string `mapstructure:"ELCHI_JWT_REFRESH_TOKEN_DURATION" yaml:"ELCHI_JWT_REFRESH_TOKEN_DURATION"`

	// CORS configuration
	ElchiCORSAllowedOrigins string `mapstructure:"ELCHI_CORS_ALLOWED_ORIGINS" yaml:"ELCHI_CORS_ALLOWED_ORIGINS"`

	// Routing configuration
	RegistryAddress string `mapstructure:"REGISTRY_ADDRESS" yaml:"REGISTRY_ADDRESS"`
	RegistryPort    uint   `mapstructure:"REGISTRY_PORT" yaml:"REGISTRY_PORT"`
}

type LoggingConfig struct {
	Level      string `mapstructure:"level"`
	Format     string `mapstructure:"format"`
	OutputPath string `mapstructure:"output_path"`
}
