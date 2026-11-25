// Package i18n holds the app's strings in English, Simplified Chinese and
// Traditional Chinese.
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Languages are the languages the app is written in.
var Languages = []string{"en", "zh-Hans", "zh-Hant"}

var lang atomic.Value

func init() { lang.Store("en") }

// SetLang sets the language strings are in; tags MyGo doesn't name as
// one of Languages fall back to English.
func SetLang(tag string) { lang.Store(Normalize(tag)) }

// Lang is the language strings are in.
func Lang() string { return lang.Load().(string) }

// Normalize maps a language tag to one of Languages.
func Normalize(tag string) string {
	t := strings.ToLower(tag)
	switch {
	case strings.HasPrefix(t, "zh-hant"), t == "zh-tw", t == "zh-hk", t == "zh-mo":
		return "zh-Hant"
	case strings.HasPrefix(t, "zh"):
		return "zh-Hans"
	}
	return "en"
}

func index() int {
	switch Lang() {
	case "zh-Hans":
		return 1
	case "zh-Hant":
		return 2
	}
	return 0
}

// T returns the string of key in the current language.
func T(key string) string {
	s, ok := dict[key]
	if !ok {
		return key
	}
	if v := s[index()]; v != "" {
		return v
	}
	return s[0]
}

// F formats the string of key with args.
func F(key string, args ...any) string { return fmt.Sprintf(T(key), args...) }

// Has reports whether key has strings, for the test of the catalog.
func Has(key string) bool {
	s, ok := dict[key]
	return ok && s[0] != "" && s[1] != "" && s[2] != ""
}

// Autonym is a language's name in itself.
func Autonym(tag string) string {
	switch tag {
	case "zh-Hans":
		return "简体中文"
	case "zh-Hant":
		return "繁體中文"
	case "en":
		return "English"
	}
	return tag
}
