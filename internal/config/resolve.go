package config

import (
	"github.com/geckoboard/bugsnag-cli/internal/apierr"
	"github.com/geckoboard/bugsnag-cli/internal/render"
)

// DefaultHost is the API for organizations on app.bugsnag.com.
const DefaultHost = "https://api.bugsnag.com"

// Flags is the subset of a flag set Resolve reads. Resolve needs Changed, not
// just the value: a flag always has a value because it has a default, so without
// Changed there is no way to tell "the user passed --format text" from "the
// user passed nothing and text is the default", and the config file would then
// lose to a default it should beat.
//
// This is the concrete reason the tool does not use viper: viper's BindPFlag has
// exactly that bug by design.
type Flags interface {
	Changed(name string) bool
	GetString(name string) (string, error)
}

// Settings is the resolved configuration for one command run.
type Settings struct {
	Token     string
	OrgID     string
	ProjectID string
	Host      string
	Format    render.Format
	TimeStyle render.TimeStyle

	// ProjectSource records where ProjectID came from, so `project show` can
	// explain itself and so autodetect knows whether it may run.
	ProjectSource Source
}

// Source is where a setting's value came from.
type Source int

const (
	SourceUnset Source = iota
	SourceFlag
	SourceConfig

	// SourceURL is a project named by a pasted dashboard URL. It applies to that
	// run only and is never written to the config file.
	SourceURL
)

func (s Source) String() string {
	switch s {
	case SourceFlag:
		return "flag"
	case SourceConfig:
		return "config"
	case SourceURL:
		return "pasted URL"
	}
	return "unset"
}

// Resolver is everything Resolve needs. It holds no filesystem or process
// state, which is what makes Resolve a pure function and the precedence matrix a
// table test.
type Resolver struct {
	Flags  Flags
	Config Config
}

// Resolve applies precedence: an explicit flag beats the config file, which
// beats the built-in default.
func Resolve(r Resolver) (Settings, error) {
	s := Settings{}

	// There is no --token flag: a token comes from the config file, written by
	// `bugsnag auth login`.
	s.Token = r.Config.Token

	orgID, _ := pick(r, "org", r.Config.Org.ID)
	s.OrgID = orgID

	// The repo-cached project is deliberately not consulted here. It is keyed by
	// repository identity, which Resolve cannot know without touching git, and
	// it is only valid while its cached OrgID matches the active organization.
	// The project resolution in internal/cli applies it, and SourceUnset means
	// "autodetect may run".
	projectID, projectSource := pick(r, "project", "")
	s.ProjectID = projectID
	s.ProjectSource = projectSource

	host, _ := pick(r, "host", r.Config.Host)
	if host == "" {
		host = DefaultHost
	}
	s.Host = host

	rawFormat, _ := pick(r, "format", "")
	format, err := render.ParseFormat(rawFormat)
	if err != nil {
		return Settings{}, apierr.Wrap(apierr.KindUsage, err, "invalid format")
	}
	s.Format = format

	rawTime, _ := pick(r, "time", "")
	timeStyle, err := render.ParseTimeStyle(rawTime)
	if err != nil {
		return Settings{}, apierr.Wrap(apierr.KindUsage, err, "invalid time style")
	}
	s.TimeStyle = timeStyle

	return s, nil
}

// pick applies the precedence chain for one setting and reports which level won.
func pick(r Resolver, flagName, configValue string) (string, Source) {
	if r.Flags != nil && r.Flags.Changed(flagName) {
		if v, err := r.Flags.GetString(flagName); err == nil && v != "" {
			return v, SourceFlag
		}
	}
	if configValue != "" {
		return configValue, SourceConfig
	}
	return "", SourceUnset
}
