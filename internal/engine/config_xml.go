package engine

import (
	"encoding/xml"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/brandnewteam/presto/internal/config"
)

type cdata struct {
	Value string `xml:",cdata"`
}

type moduleXML struct {
	XMLName          xml.Name `xml:"module"`
	Name             string   `xml:"name"`
	DisplayName      cdata    `xml:"displayName"`
	Version          cdata    `xml:"version"`
	Description      cdata    `xml:"description"`
	Author           cdata    `xml:"author"`
	AuthorURI        *cdata   `xml:"author_uri,omitempty"`
	Tab              cdata    `xml:"tab"`
	ConfirmUninstall *cdata   `xml:"confirmUninstall,omitempty"`
	IsConfigurable   int      `xml:"is_configurable"`
	NeedInstance     int      `xml:"need_instance"`
	LimitedCountries string   `xml:"limited_countries"`
}

func xmlText(value string) (cdata, error) {
	if !utf8.ValidString(value) {
		return cdata{}, fmt.Errorf("config.xml metadata must be valid UTF-8")
	}
	for _, r := range value {
		if r < 32 && r != '\t' && r != '\n' && r != '\r' || r == 0xfffe || r == 0xffff {
			return cdata{}, fmt.Errorf("config.xml metadata contains an XML control character")
		}
	}
	return cdata{value}, nil
}

func renderConfigXML(snapshot *Snapshot, settings config.ModuleXML, source string) ([]byte, error) {
	if snapshot.Version == "" {
		return nil, fmt.Errorf("config.xml requires a module version; set module.version or use build --module-version (even with --no-version)")
	}
	if !ValidModule(snapshot.Name) || !safeVersion.MatchString(snapshot.Version) {
		return nil, fmt.Errorf("invalid module identity for config.xml")
	}
	properties, configurable := phpProperties(source, snapshot.Name)
	text := func(name string, override *string, entities bool) (cdata, error) {
		value := ""
		if override != nil {
			value = *override
		} else if property, exists := properties[name]; exists {
			var ok bool
			value, ok = phpString(property.tokens)
			if !ok || property.conditional {
				return cdata{}, dynamicProperty(name)
			}
		}
		if _, err := xmlText(value); err != nil {
			return cdata{}, err
		}
		if entities {
			value = strings.ReplaceAll(html.EscapeString(value), "&amp;", "&")
		}
		return xmlText(value)
	}
	flag := func(name string, override *bool, fallback bool) (int, error) {
		if override != nil {
			return boolInt(*override), nil
		}
		if property, exists := properties[name]; exists {
			value, ok := phpBool(property.tokens)
			if !ok || property.conditional {
				return 0, dynamicProperty(name)
			}
			return boolInt(value), nil
		}
		return boolInt(fallback), nil
	}
	var result moduleXML
	var err error
	result.Name = snapshot.Name
	result.Version = cdata{snapshot.Version}
	if result.DisplayName, err = text("displayName", settings.DisplayName, true); err != nil {
		return nil, err
	}
	if result.Description, err = text("description", settings.Description, true); err != nil {
		return nil, err
	}
	if result.Author, err = text("author", settings.Author, true); err != nil {
		return nil, err
	}
	if result.Tab, err = text("tab", settings.Tab, false); err != nil {
		return nil, err
	}
	// Native PrestaShop applies HTML entities to tab, including ampersands.
	result.Tab.Value = html.EscapeString(result.Tab.Value)
	if result.IsConfigurable, err = flag("is_configurable", settings.IsConfigurable, configurable); err != nil {
		return nil, err
	}
	if result.NeedInstance, err = flag("need_instance", settings.NeedInstance, true); err != nil {
		return nil, err
	}
	uri, err := text("author_uri", settings.AuthorURI, false)
	if err != nil {
		return nil, err
	}
	if uri.Value != "" {
		uri.Value = html.EscapeString(uri.Value)
		result.AuthorURI = &uri
	}
	if _, exists := properties["confirmUninstall"]; exists || settings.ConfirmUninstall != nil {
		confirmation, err := text("confirmUninstall", settings.ConfirmUninstall, false)
		if err != nil {
			return nil, err
		}
		result.ConfirmUninstall = &confirmation
	}
	var countries []string
	if settings.LimitedCountries != nil {
		countries = *settings.LimitedCountries
	} else if property, exists := properties["limited_countries"]; exists {
		var ok bool
		countries, ok = phpCountries(property.tokens)
		if !ok || property.conditional {
			return nil, dynamicProperty("limited_countries")
		}
	}
	for _, country := range countries {
		if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			return nil, fmt.Errorf("config.xml limited_countries must contain uppercase two-letter country codes")
		}
	}
	// This is PrestaShop 1.6's convention: only a single restriction is serialized.
	if len(countries) == 1 {
		result.LimitedCountries = countries[0]
	}
	data, err := xml.MarshalIndent(result, "", "\t")
	if err != nil {
		return nil, err
	}
	return append([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\" ?>\n"), append(data, '\n')...), nil
}

// AddConfigXML generates the English metadata cache in the private prepared copy.
// Call only for build and deploy; synchronization retains its existing file rules.
func AddConfigXML(snapshot *Snapshot, settings config.ModuleXML) error {
	if snapshot.Rules.Preserved("config.xml") {
		return fmt.Errorf("cannot generate config.xml because files.preserve protects it")
	}
	for _, file := range snapshot.Files {
		if strings.EqualFold(file.Path, "config.xml") && file.Path != "config.xml" {
			return fmt.Errorf("source path %q conflicts with generated config.xml", file.Path)
		}
	}
	source, err := os.ReadFile(filepath.Join(snapshot.Root, snapshot.Name+".php"))
	if err != nil {
		return err
	}
	data, err := renderConfigXML(snapshot, settings, string(source))
	if err != nil {
		return err
	}
	destination := filepath.Join(snapshot.Root, "config.xml")
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("prepared config.xml is not a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(snapshot.Root, ".presto-config-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err := file.Chmod(0644); writeErr == nil {
		writeErr = err
	}
	if err := file.Close(); writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		return writeErr
	}
	if err := os.Rename(file.Name(), destination); err != nil {
		return err
	}
	hash, size, err := hashFile(destination)
	if err != nil {
		return err
	}
	files := make([]File, 0, len(snapshot.Files)+1)
	for _, file := range snapshot.Files {
		if file.Path != "config.xml" {
			files = append(files, file)
		}
	}
	snapshot.Files = append(files, File{Path: "config.xml", SHA256: hash, Size: size, Mode: 0644})
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	if !snapshot.Rules.Selected("config.xml") {
		snapshot.Rules.Include = append(append([]string{}, snapshot.Rules.Include...), "/config.xml")
	}
	return nil
}
