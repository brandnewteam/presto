package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brandnewteam/presto/internal/config"
)

// xmlText rejects values that cannot appear in an XML document.
func xmlText(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("config.xml metadata must be valid UTF-8")
	}
	for _, r := range value {
		if r < 32 && r != '\t' && r != '\n' && r != '\r' || r == 0xfffe || r == 0xffff {
			return "", fmt.Errorf("config.xml metadata contains an XML control character")
		}
	}
	return value, nil
}

// cdata splits any "]]>" across sections; PrestaShop would emit it unescaped and break the XML.
func cdata(value string) string {
	return "<![CDATA[" + strings.ReplaceAll(value, "]]>", "]]]]><![CDATA[>") + "]]>"
}

// renderConfigXML reproduces Module::_generateConfigXml() from PrestaShop 1.6 byte for byte.
// With translations it renders config_<iso>.xml, where native code generates the file
// from a module constructed in that language, so $this->l() values are translated.
func renderConfigXML(snapshot *Snapshot, settings config.ModuleXML, source string, translations map[string]string) ([]byte, error) {
	properties, configurable := phpProperties(source, snapshot.Name)
	if snapshot.Version == "" {
		if _, exists := properties["version"]; exists {
			return nil, fmt.Errorf("cannot statically resolve $this->version for config.xml; set module.version in TOML or use build --module-version")
		}
		return nil, fmt.Errorf("config.xml requires a module version; set module.version or use build --module-version (even with --no-version)")
	}
	if !ValidModule(snapshot.Name) || !safeVersion.MatchString(snapshot.Version) {
		return nil, fmt.Errorf("invalid module identity for config.xml")
	}
	text := func(name string, override *string) (string, error) {
		value := ""
		if override != nil {
			value = *override
		} else if property, exists := properties[name]; exists {
			var ok bool
			value, ok = phpString(property.tokens)
			if !ok || property.conditional {
				return "", dynamicProperty(name)
			}
		}
		return xmlText(value)
	}
	// A TOML override stands in for the argument of $this->l().
	localized := func(name string, override *string) (string, error) {
		if translations == nil {
			return text(name, override)
		}
		value, specific := "", new(string)
		if override != nil {
			value = *override
		} else if property, exists := properties[name]; exists {
			var ok bool
			value, specific, ok = phpLocalizable(property.tokens)
			if !ok || property.conditional {
				return "", dynamicProperty(name)
			}
		}
		if specific != nil {
			source := snapshot.Name
			if *specific != "" && *specific != "0" {
				source = *specific
			}
			value = phpModuleTranslation(translations, snapshot.Name, source, value)
		}
		return xmlText(value)
	}
	// Tools::htmlentitiesUTF8, with ampersands restored where native code does so.
	entities := func(name string, override *string, ampersands bool) (string, error) {
		read := text
		if name == "displayName" || name == "description" {
			read = localized
		}
		value, err := read(name, override)
		value = phpHTMLEntities(value)
		if ampersands {
			value = strings.ReplaceAll(value, "&amp;", "&")
		}
		return value, err
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
	displayName, err := entities("displayName", settings.DisplayName, true)
	if err != nil {
		return nil, err
	}
	description, err := entities("description", settings.Description, true)
	if err != nil {
		return nil, err
	}
	author, err := entities("author", settings.Author, true)
	if err != nil {
		return nil, err
	}
	tab, err := entities("tab", settings.Tab, false)
	if err != nil {
		return nil, err
	}
	isConfigurable, err := flag("is_configurable", settings.IsConfigurable, configurable)
	if err != nil {
		return nil, err
	}
	needInstance, err := flag("need_instance", settings.NeedInstance, true)
	if err != nil {
		return nil, err
	}
	uri, err := text("author_uri", settings.AuthorURI)
	if err != nil {
		return nil, err
	}
	authorURI := ""
	if uri != "" {
		authorURI = "<author_uri>" + cdata(phpHTMLEntities(uri)) + "</author_uri>"
	}
	confirmUninstall := ""
	if _, exists := properties["confirmUninstall"]; exists || settings.ConfirmUninstall != nil {
		confirmation, err := localized("confirmUninstall", settings.ConfirmUninstall)
		if err != nil {
			return nil, err
		}
		confirmUninstall = "\n\t<confirmUninstall>" + cdata(confirmation) + "</confirmUninstall>"
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
	limitedCountries := ""
	if len(countries) == 1 {
		limitedCountries = countries[0]
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8" ?>
<module>
	<name>` + snapshot.Name + `</name>
	<displayName>` + cdata(displayName) + `</displayName>
	<version>` + cdata(snapshot.Version) + `</version>
	<description>` + cdata(description) + `</description>
	<author>` + cdata(author) + `</author>` + authorURI + `
	<tab>` + cdata(tab) + `</tab>` + confirmUninstall + `
	<is_configurable>` + strconv.Itoa(isConfigurable) + `</is_configurable>
	<need_instance>` + strconv.Itoa(needInstance) + `</need_instance>
	<limited_countries>` + limitedCountries + `</limited_countries>
</module>`), nil
}

// AddConfigXML generates config.xml, plus config_<iso>.xml for each shipped
// translations/<iso>.php, in the private prepared copy. Call only for build and
// deploy; synchronization retains its existing file rules.
func AddConfigXML(snapshot *Snapshot, settings config.ModuleXML) error {
	languages := map[string]string{"config.xml": ""}
	for _, file := range snapshot.Files {
		if match := translationFile.FindStringSubmatch(file.Path); match != nil && match[1] != "en" {
			languages["config_"+match[1]+".xml"] = file.Path
		}
	}
	names := make([]string, 0, len(languages))
	for name := range languages {
		if snapshot.Rules.Preserved(name) {
			return fmt.Errorf("cannot generate %s because files.preserve protects it", name)
		}
		for _, file := range snapshot.Files {
			if strings.EqualFold(file.Path, name) && file.Path != name {
				return fmt.Errorf("source path %q conflicts with generated %s", file.Path, name)
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	source, err := os.ReadFile(filepath.Join(snapshot.Root, snapshot.Name+".php"))
	if err != nil {
		return err
	}
	for _, name := range names {
		var translations map[string]string
		if path := languages[name]; path != "" {
			data, err := os.ReadFile(filepath.Join(snapshot.Root, filepath.FromSlash(path)))
			if err != nil {
				return err
			}
			if translations, err = phpTranslations(string(data)); err != nil {
				return fmt.Errorf("%s: %w; it is needed for %s", path, err, name)
			}
		}
		data, err := renderConfigXML(snapshot, settings, string(source), translations)
		if err != nil {
			return err
		}
		if err := addGeneratedFile(snapshot, name, data); err != nil {
			return err
		}
	}
	return nil
}

func addGeneratedFile(snapshot *Snapshot, name string, data []byte) error {
	destination := filepath.Join(snapshot.Root, name)
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("prepared %s is not a regular file", name)
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
		if file.Path != name {
			files = append(files, file)
		}
	}
	snapshot.Files = append(files, File{Path: name, SHA256: hash, Size: size, Mode: 0644})
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	if !snapshot.Rules.Selected(name) {
		snapshot.Rules.Include = append(append([]string{}, snapshot.Rules.Include...), "/"+name)
	}
	return nil
}
