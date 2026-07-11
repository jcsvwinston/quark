package db

import (
	"fmt"

	"github.com/jcsvwinston/quark"
	"github.com/spf13/viper"
)

func GetQuarkClient() (*quark.Client, error) {
	driver := viper.GetString("database.default.driver")
	dsn := viper.GetString("database.default.dsn")

	if driver == "" || dsn == "" {
		return nil, fmt.Errorf("database configuration missing")
	}

	return quark.New(DriverName(driver), dsn, quark.WithLimits(quark.Limits{AllowRawQueries: true}))
}

func GetAdminQuarkClient() (*quark.Client, error) {
	driver := viper.GetString("database.admin.driver")
	dsn := viper.GetString("database.admin.dsn")

	if driver == "" || dsn == "" {
		return nil, fmt.Errorf("admin database configuration missing")
	}

	return quark.New(DriverName(driver), dsn, quark.WithLimits(quark.Limits{AllowRawQueries: true}))
}
