package pkg

type MatrixConfig struct {
	Url         string `yaml:"url"`
	User        string `yaml:"user"`
	AccessToken string `yaml:"access_token"`
	Room        string `yaml:"room"`
	SqliteDb    string `yaml:"sqlite_db"`
	Password    string `yaml:"password"`
	Passphrase  string `yaml:"passphrase"`
	Bot         string `yaml:"bot"`
}
