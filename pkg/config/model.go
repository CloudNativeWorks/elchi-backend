package config

type AppConfig struct {
	ElchiAddress               string   `mapstructure:"ELCHI_ADDRESS" yaml:"ELCHI_ADDRESS"`
	ElchiPort                  string   `mapstructure:"ELCHI_PORT" yaml:"ELCHI_PORT"`
	ElchiTLSEnabled            string   `mapstructure:"ELCHI_TLS_ENABLED" yaml:"ELCHI_TLS_ENABLED"`
	ElchiEnableDemo            string   `mapstructure:"ELCHI_ENABLE_DEMO" yaml:"ELCHI_ENABLE_DEMO"`
	ElchiVersions              []string `mapstructure:"ELCHI_VERSIONS" yaml:"ELCHI_VERSIONS"`
	ElchiInternalCommunication string   `mapstructure:"ELCHI_INTERNAL_COMMUNICATION" yaml:"ELCHI_INTERNAL_COMMUNICATION"`
	ElchiInternalAddressPort   string   `mapstructure:"ELCHI_INTERNAL_ADDRESS_PORT" yaml:"ELCHI_INTERNAL_ADDRESS_PORT"`

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

/* 	LogLevel        string `mapstructure:"LOG_LEVEL" yaml:"LOG_LEVEL"`
	LogFormatter    string `mapstructure:"LOG_FORMATTER" yaml:"LOG_FORMATTER"`
	LogReportCaller string `mapstructure:"LOG_REPORTCALLER" yaml:"LOG_REPORTCALLER"` */

	Logging LoggingConfig `mapstructure:"LOGGING" yaml:"LOGGING"`

	SMTPPassword string `mapstructure:"SMTP_PASSWORD" yaml:"SMTP_PASSWORD"`
}

type LoggingConfig struct {
	Level      string            `mapstructure:"level"`
	Format     string            `mapstructure:"format"`
	OutputPath string            `mapstructure:"output_path"`
	Modules    map[string]string `mapstructure:"modules"`
}