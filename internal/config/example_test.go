package config

import "testing"

// The example shipped in the repo must stay loadable.
func TestExampleConfig(t *testing.T) {
	c, err := Load("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Effective("gitlab.example.com/team/app")
	if err != nil || s.Mode != ModeOpen || s.Language.Comments != "pl" || s.Language.Brief != "en" {
		t.Errorf("%+v %v", s, err)
	}
}
