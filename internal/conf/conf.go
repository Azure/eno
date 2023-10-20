package conf

import "time"

type Config struct {
	WrapperImage string `split_words:"true" required:"true"`
	Namespace    string `split_words:"true" default:"default"`

	// Jobs are actually just boring old pods now - should we change these field names?
	JobTimeout time.Duration `split_words:"true" default:"30s"`
	// TODO(mariano): Evaluate if this should be scoped to the generator.
	JobSA string `split_words:"true" default:""`

	StatusPollingInterval time.Duration `split_words:"true" default:"10s"`
	RolloutCooldown       time.Duration `split_words:"true" default:"30s"`
}
