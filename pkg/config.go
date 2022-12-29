package pkg

import "maunium.net/go/mautrix/appservice"

type MatrixConfig struct {
	User             string                 `yaml:"user"`
	Room             string                 `yaml:"room"`
	DisplayName      string                 `yaml:"display_name"`
	AccessToken      string                 `yaml:"access_token"`
	SqliteDb         string                 `yaml:"sqlite_db"`
	Db               string                 `yaml:"db"`
	ConnectionString string                 `yaml:"connection_string"`
	Passphrase       string                 `yaml:"passphrase"`
	AppService       *appservice.AppService `yaml:"app_service"`
	BridgeConfigPath string                 `yaml:"bridgeConfig"`
	ImageDisplay     struct {
		Enabled        bool     `yaml:"enabled"`
		AllowedDomains []string `yaml:"allowed_domains"`
	} `yaml:"image_display"`
}
