package config

import "strings"

// ParseKeyValue parses a single key=value pair and returns the key and value.
// If no value is provided, the value will be empty.
func ParseKeyValue(input string) (key, val string) {
	chunks := strings.SplitN(input, "=", 2)
	key = chunks[0]
	if len(chunks) > 1 {
		val = chunks[1]
	}
	return
}

// ParseKeyValuePairs parses a comma-separated string of key=value pairs
// and returns them as a map. Empty pairs are ignored, and whitespace
// around pairs is trimmed.
func ParseKeyValuePairs(input string) map[string]string {
	result := make(map[string]string)
	if input == "" {
		return result
	}

	pairs := strings.Split(input, ",")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, val := ParseKeyValue(pair)
		if key != "" {
			result[key] = val
		}
	}
	return result
}
