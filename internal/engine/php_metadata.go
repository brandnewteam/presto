package engine

import (
	"fmt"
	"strings"
)

type phpToken struct {
	text  string
	value *string
}

// phpTokens reads a small data-only subset, never evaluating PHP. Quoted strings,
// comments and heredocs are consumed as units so their contents cannot be code.
func phpTokens(source string) []phpToken {
	var result []phpToken
	for i := 0; i < len(source); {
		c := source[i]
		if strings.ContainsRune(" \t\r\n", rune(c)) {
			i++
			continue
		}
		if strings.HasPrefix(source[i:], "/*") {
			end := strings.Index(source[i+2:], "*/")
			if end < 0 {
				break
			}
			i += end + 4
			continue
		}
		if c == '#' || strings.HasPrefix(source[i:], "//") {
			end := strings.IndexByte(source[i:], '\n')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		if strings.HasPrefix(source[i:], "<<<") {
			end := strings.IndexByte(source[i:], '\n')
			if end < 0 {
				break
			}
			label := strings.Trim(strings.TrimSpace(source[i+3:i+end]), "\"'")
			i += end + 1
			for i < len(source) {
				end = strings.IndexByte(source[i:], '\n')
				if end < 0 {
					end = len(source) - i
				}
				line := strings.TrimSpace(source[i : i+end])
				i += end
				if i < len(source) {
					i++
				}
				if line == label || strings.HasPrefix(line, label+";") || strings.HasPrefix(line, label+",") || strings.HasPrefix(line, label+")") {
					break
				}
			}
			result = append(result, phpToken{text: "dynamic"})
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote := c
			i++
			var value strings.Builder
			valid := quote != '`'
			closed := false
			for i < len(source) {
				c = source[i]
				i++
				if c == quote {
					closed = true
					break
				}
				if quote != '\'' && c == '$' {
					valid = false
				}
				if c != '\\' || i == len(source) {
					value.WriteByte(c)
					continue
				}
				next := source[i]
				i++
				if quote == '\'' {
					if next != '\'' && next != '\\' {
						value.WriteByte('\\')
					}
					value.WriteByte(next)
					continue
				}
				switch next {
				case 'n':
					value.WriteByte('\n')
				case 'r':
					value.WriteByte('\r')
				case 't':
					value.WriteByte('\t')
				case 'v':
					value.WriteByte('\v')
				case 'f':
					value.WriteByte('\f')
				case 'e':
					value.WriteByte(27)
				case '\\', '"', '$':
					value.WriteByte(next)
				default:
					// Less common PHP escape forms must be made explicit in TOML.
					if next == 'x' || next == 'u' || next >= '0' && next <= '7' {
						valid = false
					}
					value.WriteByte('\\')
					value.WriteByte(next)
				}
			}
			token := phpToken{text: "string"}
			if valid && closed {
				text := value.String()
				token.value = &text
			}
			result = append(result, token)
			continue
		}
		if strings.HasPrefix(source[i:], "->") {
			result = append(result, phpToken{text: "->"})
			i += 2
			continue
		}
		start := i
		if c == '$' || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			i++
			for i < len(source) {
				c = source[i]
				if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
					break
				}
				i++
			}
		} else {
			i++
		}
		result = append(result, phpToken{text: source[start:i]})
	}
	return result
}

type phpProperty struct {
	tokens      []phpToken
	conditional bool
}

func phpProperties(source, module string) (map[string]phpProperty, bool) {
	tokens := phpTokens(source)
	foundClass := false
	for i := 0; i+1 < len(tokens); i++ {
		if !strings.EqualFold(tokens[i].text, "class") || !strings.EqualFold(tokens[i+1].text, module) {
			continue
		}
		start := i + 2
		for start < len(tokens) && tokens[start].text != "{" {
			start++
		}
		depth := 1
		end := start + 1
		for end < len(tokens) {
			if tokens[end].text == "{" {
				depth++
			}
			if tokens[end].text == "}" {
				depth--
			}
			if depth == 0 {
				break
			}
			end++
		}
		if start < len(tokens) {
			tokens = tokens[start+1 : end]
			foundClass = true
		}
		break
	}
	properties := map[string]phpProperty{}
	configurable := false
	constructorStart, constructorEnd := -1, -1
	for i := 0; i+2 < len(tokens); i++ {
		if strings.EqualFold(tokens[i].text, "function") && tokens[i+2].text == "(" {
			if strings.EqualFold(tokens[i+1].text, "getContent") {
				configurable = true
			}
			if strings.EqualFold(tokens[i+1].text, "__construct") {
				for j := i + 3; j < len(tokens); j++ {
					if tokens[j].text == "{" {
						constructorStart = j + 1
						break
					}
				}
				depth := 1
				for j := constructorStart; constructorStart >= 0 && j < len(tokens); j++ {
					if tokens[j].text == "{" {
						depth++
					}
					if tokens[j].text == "}" {
						depth--
					}
					if depth == 0 {
						constructorEnd = j
						break
					}
				}
			}
		}
	}
	// Read class property defaults before constructor assignments override them.
	for i := 1; i+2 < len(tokens); i++ {
		if (tokens[i-1].text == "public" || tokens[i-1].text == "protected" || tokens[i-1].text == "var") && strings.HasPrefix(tokens[i].text, "$") && tokens[i+1].text == "=" {
			end := i + 2
			for end < len(tokens) && tokens[end].text != ";" {
				end++
			}
			properties[strings.TrimPrefix(tokens[i].text, "$")] = phpProperty{tokens: tokens[i+2 : end]}
		}
	}
	if constructorStart >= 0 && constructorEnd >= constructorStart {
		tokens = tokens[constructorStart:constructorEnd]
	} else if foundClass {
		return properties, configurable
	} else {
		for _, token := range tokens {
			if strings.EqualFold(token.text, "class") {
				return properties, configurable
			}
		}
	}
	depth := 0
	assigned := map[string]bool{}
	conditional := false
	for i := 0; i+3 < len(tokens); i++ {
		if tokens[i].text == "{" {
			depth++
		}
		if tokens[i].text == "}" {
			depth--
		}
		switch strings.ToLower(tokens[i].text) {
		case "if", "elseif", "else", "switch", "for", "foreach", "while", "do", "try", "catch":
			conditional = true
		}
		if tokens[i].text != "$this" || tokens[i+1].text != "->" || tokens[i+3].text != "=" {
			continue
		}
		end := i + 4
		for end < len(tokens) && tokens[end].text != ";" {
			end++
		}
		name := tokens[i+2].text
		properties[name] = phpProperty{tokens: tokens[i+4 : end], conditional: depth != 0 || conditional || assigned[name]}
		assigned[name] = true
	}
	return properties, configurable
}

func phpString(tokens []phpToken) (string, bool) {
	if len(tokens) == 1 && strings.EqualFold(tokens[0].text, "null") {
		return "", true
	}
	if len(tokens) == 1 && tokens[0].value != nil {
		return *tokens[0].value, true
	}
	if len(tokens) >= 6 && tokens[0].text == "$this" && tokens[1].text == "->" && strings.EqualFold(tokens[2].text, "l") && tokens[3].text == "(" && tokens[len(tokens)-1].text == ")" {
		args := tokens[4 : len(tokens)-1]
		if len(args) == 1 || len(args) == 3 && args[1].text == "," && args[2].value != nil {
			return phpString(args[:1])
		}
	}
	return "", false
}

func phpBool(tokens []phpToken) (bool, bool) {
	if len(tokens) != 1 {
		return false, false
	}
	switch strings.ToLower(tokens[0].text) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

func phpCountries(tokens []phpToken) ([]string, bool) {
	if len(tokens) >= 2 && tokens[0].text == "[" && tokens[len(tokens)-1].text == "]" {
		tokens = tokens[1 : len(tokens)-1]
	} else if len(tokens) >= 3 && strings.EqualFold(tokens[0].text, "array") && tokens[1].text == "(" && tokens[len(tokens)-1].text == ")" {
		tokens = tokens[2 : len(tokens)-1]
	} else {
		return nil, false
	}
	var values []string
	for i := 0; i < len(tokens); i += 2 {
		value, ok := phpString(tokens[i : i+1])
		if !ok {
			return nil, false
		}
		values = append(values, value)
		if i+1 < len(tokens) && tokens[i+1].text != "," {
			return nil, false
		}
	}
	return values, true
}

func dynamicProperty(name string) error {
	return fmt.Errorf("cannot statically resolve $this->%s for config.xml; set module.config_xml.%s in TOML", name, phpOverrideName(name))
}

func phpOverrideName(name string) string {
	switch name {
	case "displayName":
		return "display_name"
	case "confirmUninstall":
		return "confirm_uninstall"
	}
	return name
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
