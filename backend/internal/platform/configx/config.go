package configx

import (
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	HTTP struct {
		Addr   string `mapstructure:"addr"`
		APIKey string `mapstructure:"api_key"`
	} `mapstructure:"http"`
	MySQL struct {
		DSN string `mapstructure:"dsn"`
	} `mapstructure:"mysql"`
	Redis struct {
		Addr string `mapstructure:"addr"`
	} `mapstructure:"redis"`
	Kafka struct {
		Brokers       []string `mapstructure:"brokers"`
		GroupID       string   `mapstructure:"group_id"`
		PublishTopic  string   `mapstructure:"publish_topic"`
		ConsumeTopics []string `mapstructure:"consume_topics"`
	} `mapstructure:"kafka"`
}

func Load(path, envPrefix string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	if err := v.ReadInConfig(); err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
