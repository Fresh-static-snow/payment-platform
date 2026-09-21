package kafkax

import (
	"strings"

	"github.com/segmentio/kafka-go"
)

type HeaderCarrier []kafka.Header

func (c HeaderCarrier) Get(key string) string {
	for _, item := range c {
		if strings.EqualFold(item.Key, key) {
			return string(item.Value)
		}
	}
	return ""
}

func (c HeaderCarrier) Set(string, string) {}

func (c HeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for _, item := range c {
		keys = append(keys, item.Key)
	}
	return keys
}
