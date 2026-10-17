package main

import (
	"encoding/xml"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/firestarter2501/unraid_exporter/internal/collector"
)

type unraidTemplate struct {
	Repository  string `xml:"Repository"`
	Network     string `xml:"Network"`
	WebUI       string `xml:"WebUI"`
	ExtraParams string `xml:"ExtraParams"`
	Configs     []struct {
		Name     string `xml:"Name,attr"`
		Target   string `xml:"Target,attr"`
		Default  string `xml:"Default,attr"`
		Type     string `xml:"Type,attr"`
		Required string `xml:"Required,attr"`
		Mask     string `xml:"Mask,attr"`
		Value    string `xml:",chardata"`
	} `xml:"Config"`
}

// TestUnraidTemplate keeps deploy/unraid/unraid-exporter.xml in sync with the
// exporter's environment variables and defaults.
func TestUnraidTemplate(t *testing.T) {
	b, err := os.ReadFile("../../deploy/unraid/unraid-exporter.xml")
	if err != nil {
		t.Fatal(err)
	}
	var tpl unraidTemplate
	if err := xml.Unmarshal(b, &tpl); err != nil {
		t.Fatalf("template is not valid XML: %v", err)
	}
	if tpl.Repository != "ghcr.io/firestarter2501/unraid_exporter:latest" {
		t.Errorf("Repository = %q, must be the image published by .github/workflows/ci.yml", tpl.Repository)
	}
	if tpl.Network != "bridge" {
		t.Errorf("Network = %q", tpl.Network)
	}

	var raw rawFlags
	fs := newFlagSet(&config{}, &raw)
	envToFlag := map[string]string{}
	for _, e := range envVars {
		envToFlag[e.env] = e.flag
	}

	seen := map[string]bool{}
	for _, c := range tpl.Configs {
		// dockerMan renders a Variable whose Default contains "|" as a
		// dropdown of the "|"-separated parts, which breaks regexps.
		if strings.Contains(c.Default, "|") || strings.Contains(c.Value, "|") {
			t.Errorf("%s: Default/value must not contain '|' (Unraid turns it into a dropdown)", c.Target)
		}
		switch c.Type {
		case "Port":
			if !strings.HasSuffix(fs.Lookup("web.listen-address").DefValue, ":"+c.Target) {
				t.Errorf("port %s does not match the default listen address", c.Target)
			}
			if !strings.Contains(tpl.WebUI, "[PORT:"+c.Target+"]") {
				t.Errorf("WebUI %q does not use port %s", tpl.WebUI, c.Target)
			}
		case "Variable":
			flagName, ok := envToFlag[c.Target]
			if !ok {
				t.Errorf("template variable %s is not an exporter environment variable", c.Target)
				continue
			}
			seen[c.Target] = true
			if c.Target == "UNRAID_EXPORTER_COLLECTORS" {
				// The template may enable more collectors than the exporter
				// default (e.g. disks), but only existing ones.
				for _, name := range strings.Split(c.Default, ",") {
					if !slices.Contains(collector.Available(), name) {
						t.Errorf("template enables unknown collector %q", name)
					}
				}
			} else if def := fs.Lookup(flagName).DefValue; c.Default != "" && c.Default != def {
				t.Errorf("%s: template default %q, exporter default %q", c.Target, c.Default, def)
			}
			if c.Value != "" && c.Value != c.Default {
				t.Errorf("%s: template value %q differs from its default %q", c.Target, c.Value, c.Default)
			}
		default:
			t.Errorf("unexpected config type %q for %s", c.Type, c.Name)
		}
	}
	for _, env := range []string{"UNRAID_URL", "UNRAID_API_KEY"} {
		if !seen[env] {
			t.Errorf("template is missing required variable %s", env)
		}
	}
	for _, c := range tpl.Configs {
		if c.Target == "UNRAID_API_KEY" && c.Mask != "true" {
			t.Error("UNRAID_API_KEY must be masked in the Unraid UI")
		}
	}
}
