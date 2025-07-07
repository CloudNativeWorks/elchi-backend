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

	return &appConfig
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
