package conf

import "time"

type Config struct {
	WrapperImage string `split_words:"true" required:"true"`
	Namespace    string `split_words:"true" default:"default"`

	JobTimeout time.Duration `split_words:"true" default:"30s"`
	JobTTL     time.Duration `split_words:"true" default:"5m"`
	// TODO(mariano): Evaluate if this should be scoped to the generator.
	JobSA string `split_words:"true" default:""`

	StatusPollingInterval time.Duration `split_words:"true" default:"10s"`
}
