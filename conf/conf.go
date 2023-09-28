package conf

import "time"

type Config struct {
	WrapperImage string `split_words:"true" required:"true"`

	JobTimeout time.Duration `split_words:"true" default:"30s"`
	JobTTL     time.Duration `split_words:"true" default:"5m"`
	JobNS      string        `split_words:"true" default:"default"`

	StatusPollingInterval time.Duration `split_words:"true" default:"10s"`
}
