package config

import (
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

var loggingCases = []struct {
	name      string
	level     string
	format    string
	want      Config
	wantError string
}{
	{name: "default console with independent level", level: "info", format: "", want: Config{LogFormat: "console", LogLevel: zerolog.InfoLevel}},
	{name: "explicit JSON", level: "warn", format: "json", want: Config{LogFormat: "json", LogLevel: zerolog.WarnLevel}},
	{name: "invalid level", level: "verbose", format: "console", wantError: "parse LOG_LEVEL"},
	{name: "invalid format", level: "info", format: "text", wantError: "invalid LOG_FORMAT"},
}

func TestLoadLoggingConfiguration(t *testing.T) {
	previous := Cfg
	t.Cleanup(func() { Cfg = previous })
	for _, testCase := range loggingCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("LOG_FORMAT", testCase.format)
			t.Setenv("LOG_LEVEL", testCase.level)
			err := Load()
			if testCase.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
					t.Errorf("Load error = %v, want %s", err, testCase.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("load configuration: %v", err)
			}
			if *Cfg != testCase.want {
				t.Errorf("configuration = %#v, want %#v", Cfg, testCase.want)
			}
		})
	}
}
