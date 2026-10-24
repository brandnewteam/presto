package config

// ModuleXML overrides metadata that cannot be read as static PHP literals.
type ModuleXML struct {
	DisplayName      *string   `toml:"display_name"`
	Description      *string   `toml:"description"`
	Author           *string   `toml:"author"`
	AuthorURI        *string   `toml:"author_uri"`
	Tab              *string   `toml:"tab"`
	ConfirmUninstall *string   `toml:"confirm_uninstall"`
	IsConfigurable   *bool     `toml:"is_configurable"`
	NeedInstance     *bool     `toml:"need_instance"`
	LimitedCountries *[]string `toml:"limited_countries"`
}

func override[T any](previous, next *T) *T {
	if next != nil {
		return next
	}
	return previous
}

func (m ModuleXML) merge(next ModuleXML) ModuleXML {
	return ModuleXML{
		override(m.DisplayName, next.DisplayName), override(m.Description, next.Description),
		override(m.Author, next.Author), override(m.AuthorURI, next.AuthorURI),
		override(m.Tab, next.Tab), override(m.ConfirmUninstall, next.ConfirmUninstall),
		override(m.IsConfigurable, next.IsConfigurable), override(m.NeedInstance, next.NeedInstance),
		override(m.LimitedCountries, next.LimitedCountries),
	}
}
