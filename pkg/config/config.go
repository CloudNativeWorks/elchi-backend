// Package config provides configuration management using Viper
// for loading and accessing application settings from files and environment.
package config

import (
	"log"
	"os"
	"reflect"
	"strings"

	"github.com/spf13/viper"
)

func Read(cfgFile string) *AppConfig {
	var appConfig AppConfig
	viper.SetConfigType("yaml")

	if _, found := os.LookupEnv("isKBs"); found {
		BindEnvs(&appConfig)
	} else {
		viper.SetConfigFile(cfgFile)
		if err := viper.ReadInConfig(); err != nil {
			panic(err)
		}
	}

	if err := viper.Unmarshal(&appConfig); err != nil {
		panic(err)
	}

	applyDefaults(&appConfig)
	return &appConfig
}

// applyDefaults fills in built-in fallbacks for optional fields whose
// upstream behavior depends on a non-zero value. Keeping defaults out of
// the load path means an operator can clear a value by leaving the
// env/yaml entry blank — only zero-valued fields get patched here.
func applyDefaults(c *AppConfig) {
	// ClickHouse — names match the collector's defaults so a single
	// secret bundle wires both services. URI intentionally has no
	// default; an empty URI tells the controller to skip ClickHouse
	// initialization (inventory_detail endpoints will respond 503).
	if c.ClickhouseDatabase == "" {
		c.ClickhouseDatabase = "elchi"
	}
	if c.ClickhouseTable == "" {
		c.ClickhouseTable = "api_events_raw"
	}
	if c.ClickhouseShieldTable == "" {
		c.ClickhouseShieldTable = "elchi_shield_audit"
	}
	if c.ClickhouseRollup1m == "" {
		c.ClickhouseRollup1m = "api_events_1m"
	}
	if c.ClickhouseRollup1h == "" {
		c.ClickhouseRollup1h = "api_events_1h"
	}
	if c.ClickhouseRollup1d == "" {
		c.ClickhouseRollup1d = "api_events_1d"
	}
	if c.ClickhouseConnectTimeoutSec == 0 {
		c.ClickhouseConnectTimeoutSec = 5
	}
	if c.ClickhouseQueryTimeoutSec == 0 {
		c.ClickhouseQueryTimeoutSec = 30
	}
	// Pool defaults: the driver's built-in caps (idle=5, open=10) are
	// far below the inventory geo handlers' peak — a single dashboard
	// load fires up to seven parallel scans, two concurrent users
	// would queue. 20 idle keeps a hot pool ready; 50 open caps burst
	// under load. ConnMaxLifetime recycles connections after an hour
	// so LB/credential rotations don't leave half-dead sockets in the
	// pool.
	if c.ClickhouseMaxIdleConns == 0 {
		c.ClickhouseMaxIdleConns = 20
	}
	if c.ClickhouseMaxOpenConns == 0 {
		c.ClickhouseMaxOpenConns = 50
	}
	if c.ClickhouseConnMaxLifetimeMin == 0 {
		c.ClickhouseConnMaxLifetimeMin = 60
	}
}

func BindEnvs(iface any) {
	val := reflect.ValueOf(iface).Elem()
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := typ.Field(i)
		if mapstructureTag, ok := field.Tag.Lookup("mapstructure"); ok {
			envVar := strings.ToUpper(mapstructureTag)
			err := viper.BindEnv(field.Name, envVar)
			if err != nil {
				log.Printf("[INFO] Error binding env var %s to field %s", envVar, field.Name)
			}
		}
	}
}
