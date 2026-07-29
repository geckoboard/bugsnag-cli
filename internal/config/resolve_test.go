package config_test

import (
	"fmt"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/geckoboard/bugsnag-cli/internal/config"
	"github.com/geckoboard/bugsnag-cli/internal/exitcode"
	"github.com/geckoboard/bugsnag-cli/internal/render"
)

// fakeFlags models a flag set the way pflag behaves: every flag has a value
// because it has a default, and Changed reports whether the user actually passed
// it. Keeping those separate is the whole point of the Flags interface.
type fakeFlags struct {
	values  map[string]string
	changed map[string]bool
}

func (f fakeFlags) Changed(name string) bool { return f.changed[name] }

func (f fakeFlags) GetString(name string) (string, error) {
	v, ok := f.values[name]
	if !ok {
		return "", fmt.Errorf("no such flag %q", name)
	}
	return v, nil
}

// flags builds a flag set where the named flags were explicitly passed.
func flags(passed map[string]string) fakeFlags {
	f := fakeFlags{values: map[string]string{}, changed: map[string]bool{}}
	for k, v := range passed {
		f.values[k] = v
		f.changed[k] = true
	}
	return f
}

// TestResolvePrecedence is the whole precedence matrix. It is a table test
// because Resolve is a pure function: no filesystem, no globals, nothing to reset
// between cases.
func TestResolvePrecedence(t *testing.T) {
	const (
		flagValue   = "from-flag"
		configValue = "from-config"
	)

	// Exercised through --org, which is a real persistent flag with a real config
	// counterpart. The precedence code is shared by every setting.
	for _, tc := range []struct {
		name  string
		flags config.Flags
		cfg   config.Config
		want  string
	}{
		{
			name:  "flag beats config",
			flags: flags(map[string]string{"org": flagValue}),
			cfg:   config.Config{Org: config.Org{ID: configValue}},
			want:  flagValue,
		},
		{
			name: "config is used when nothing else is set",
			cfg:  config.Config{Org: config.Org{ID: configValue}},
			want: configValue,
		},
		{
			name: "nothing set",
			want: "",
		},
		{
			// This is the case viper's BindPFlag gets wrong: an unpassed flag
			// still has its default value, and treating that as user intent lets
			// a default beat a config value that was set deliberately.
			name: "an unpassed flag does not beat the config",
			flags: fakeFlags{
				values:  map[string]string{"org": "flag-default"},
				changed: map[string]bool{},
			},
			cfg:  config.Config{Org: config.Org{ID: configValue}},
			want: configValue,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := config.Resolve(config.Resolver{Flags: tc.flags, Config: tc.cfg})
			assert.NilError(t, err)
			assert.Equal(t, got.OrgID, tc.want)
		})
	}
}

// TestResolveTokenComesFromTheConfig: there is no --token flag, so a token can
// only have been written by `bugsnag auth login`.
func TestResolveTokenComesFromTheConfig(t *testing.T) {

	got, err := config.Resolve(config.Resolver{
		Flags:  flags(map[string]string{"token": "from-flag"}),
		Config: config.Config{Token: "from-config"},
	})
	assert.NilError(t, err)
	assert.Equal(t, got.Token, "from-config")
}

// TestResolveProjectSourceIsReported: autodetect must only run when the project
// was not specified, so Resolve has to say where the value came from rather than
// just what it is.
func TestResolveProjectSourceIsReported(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flags  config.Flags
		want   string
		source config.Source
	}{
		{
			name:   "flag",
			flags:  flags(map[string]string{"project": "p-flag"}),
			want:   "p-flag",
			source: config.SourceFlag,
		},
		{
			name:   "unset leaves autodetect free to run",
			want:   "",
			source: config.SourceUnset,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := config.Resolve(config.Resolver{Flags: tc.flags})
			assert.NilError(t, err)
			assert.Equal(t, got.ProjectID, tc.want)
			assert.Equal(t, got.ProjectSource, tc.source)
		})
	}
}

// TestResolveHostDefault: the default must be the real API. The spec lists a
// SwaggerHub mock server first, and that must never be what the CLI talks to.
func TestResolveHostDefault(t *testing.T) {
	got, err := config.Resolve(config.Resolver{})
	assert.NilError(t, err)
	assert.Equal(t, got.Host, config.DefaultHost)
	assert.Equal(t, got.Host, "https://api.bugsnag.com")
}

func TestResolveHostFromConfig(t *testing.T) {
	const smartbear = "https://api.bugsnag.smartbear.com"
	got, err := config.Resolve(config.Resolver{Config: config.Config{Host: smartbear}})
	assert.NilError(t, err)
	assert.Equal(t, got.Host, smartbear)
}

// TestResolveFormatDefaultIsText: the default is text, not JSON. Text is several
// times smaller on real payloads and --json is still four characters away.
func TestResolveFormatDefaultIsText(t *testing.T) {
	got, err := config.Resolve(config.Resolver{})
	assert.NilError(t, err)
	assert.Equal(t, got.Format, render.FormatText)
}

func TestResolveFormat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags config.Flags
		want  render.Format
	}{
		{"flag json", flags(map[string]string{"format": "json"}), render.FormatJSON},
		{"flag text", flags(map[string]string{"format": "text"}), render.FormatText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := config.Resolve(config.Resolver{Flags: tc.flags})
			assert.NilError(t, err)
			assert.Equal(t, got.Format, tc.want)
		})
	}
}

func TestResolveInvalidFormatIsAUsageError(t *testing.T) {
	_, err := config.Resolve(config.Resolver{Flags: flags(map[string]string{"format": "yaml"})})
	assert.Assert(t, err != nil, "expected an error for an unknown format")
	assert.Equal(t, exitcode.Of(err), exitcode.Usage)
}

func TestResolveTimeStyle(t *testing.T) {
	for _, tc := range []struct {
		flag string
		want render.TimeStyle
	}{
		{"", render.TimeAuto},
		{"relative", render.TimeRelative},
		{"local", render.TimeLocal},
		{"raw", render.TimeRaw},
	} {
		name := tc.flag
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			r := config.Resolver{}
			if tc.flag != "" {
				r.Flags = flags(map[string]string{"time": tc.flag})
			}
			got, err := config.Resolve(r)
			assert.NilError(t, err)
			assert.Equal(t, got.TimeStyle, tc.want)
		})
	}
}
