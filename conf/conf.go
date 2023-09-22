package conf

import "time"

type Config struct {
	WrapperImage string `split_words:"true" required:"true"`

	JobTimeout time.Duration `split_words:"true" required:"1m"`
	JobTTL     time.Duration `split_words:"true" default:"1m"`
	JobNS      string        `split_words:"true" default:"default"`
}
