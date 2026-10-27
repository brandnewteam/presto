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

// phpProperties reads class defaults and constructor assignments of the module class.
// Only assignments that always execute are static: anything inside a block, a braceless
// control statement, alternative control syntax or after an early exit is conditional.
// A later unconditional assignment replaces earlier values, as it does in PHP.
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
			tokens = tokens[start+1 : min(end, len(tokens))]
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
	depth := 0
	for i := 0; i+1 < len(tokens); i++ {
		switch tokens[i].text {
		case "{":
			depth++
		case "}":
			depth--
		}
		if depth == 0 && strings.HasPrefix(tokens[i].text, "$") && tokens[i+1].text == "=" && phpPropertyDeclaration(tokens, i) {
			properties[strings.TrimPrefix(tokens[i].text, "$")] = phpProperty{tokens: tokens[i+2 : phpStatementEnd(tokens, i+2)]}
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
	phpAssignments(tokens, properties)
	return properties, configurable
}

func phpAssignments(tokens []phpToken, properties map[string]phpProperty) {
	depth, alternative := 0, 0
	var closures, braceless []int
	pendingClosure, exited := false, false
	conditionEnd := -1
	conditionKeyword := ""
	// A control statement's body starts after its keyword or parenthesized condition.
	body := func(keyword string, next int) {
		if next >= len(tokens) {
			return
		}
		switch tokens[next].text {
		case "{":
			// Every block is conditional through depth.
		case ":":
			if keyword != "else" && keyword != "elseif" {
				alternative++
			}
		default:
			if !(keyword == "else" && strings.EqualFold(tokens[next].text, "if")) {
				braceless = append(braceless, depth)
			}
		}
	}
	endStatements := func() {
		for len(braceless) > 0 && braceless[len(braceless)-1] >= depth {
			braceless = braceless[:len(braceless)-1]
		}
	}
	for i := 0; i < len(tokens); i++ {
		text := strings.ToLower(tokens[i].text)
		if i == conditionEnd {
			conditionEnd = -1
			body(conditionKeyword, i+1)
			continue
		}
		switch text {
		case "{":
			depth++
			if pendingClosure {
				closures, pendingClosure = append(closures, depth), false
			}
			continue
		case "}":
			if len(closures) > 0 && closures[len(closures)-1] == depth {
				closures = closures[:len(closures)-1]
			}
			depth--
			endStatements()
			continue
		case ";":
			endStatements()
			continue
		case "function":
			pendingClosure = true
			continue
		case "return", "exit", "die", "throw":
			if len(closures) == 0 {
				exited = true
			}
			continue
		case "if", "elseif", "while", "for", "foreach", "switch", "catch":
			if i+1 < len(tokens) && tokens[i+1].text == "(" && conditionEnd < 0 {
				conditionEnd, conditionKeyword = phpClosingParen(tokens, i+1), text
			}
			continue
		case "else", "do", "try", "finally":
			body(text, i+1)
			continue
		case "endif", "endwhile", "endfor", "endforeach", "endswitch":
			if alternative > 0 {
				alternative--
			}
			continue
		}
		if tokens[i].text != "$this" || i+3 >= len(tokens) || tokens[i+1].text != "->" || !phpIdentifier(tokens[i+2].text) {
			continue
		}
		name := tokens[i+2].text
		conditional := depth > 0 || len(braceless) > 0 || alternative > 0 || exited || conditionEnd >= 0
		if phpModifies(tokens, i+3) {
			properties[name] = phpProperty{tokens: []phpToken{{text: "dynamic"}}, conditional: true}
			continue
		}
		if tokens[i+3].text != "=" || i+4 < len(tokens) && tokens[i+4].text == "=" {
			continue
		}
		value := tokens[i+4 : phpStatementEnd(tokens, i+4)]
		if conditional {
			properties[name] = phpProperty{tokens: value, conditional: true}
		} else {
			properties[name] = phpProperty{tokens: value}
		}
	}
}

// phpModifies recognizes compound and element assignments, which are never static.
func phpModifies(tokens []phpToken, i int) bool {
	at := func(offset int, text string) bool { return i+offset < len(tokens) && tokens[i+offset].text == text }
	switch tokens[i].text {
	case ".", "+", "-", "*", "/", "%", "|", "&", "^":
		return at(1, "=") || (tokens[i].text == "+" && at(1, "+")) || (tokens[i].text == "-" && at(1, "-"))
	case "?", "<", ">":
		return at(1, tokens[i].text) && at(2, "=")
	case "[":
		end := i
		for depth := 0; end < len(tokens); end++ {
			if tokens[end].text == "[" {
				depth++
			} else if tokens[end].text == "]" {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		return end+1 < len(tokens) && tokens[end+1].text == "=" && !(end+2 < len(tokens) && tokens[end+2].text == "=")
	}
	return false
}

func phpPropertyDeclaration(tokens []phpToken, i int) bool {
	j := i - 1
	for j >= 0 && (tokens[j].text == "?" || tokens[j].text == "|" || tokens[j].text == "\\" || phpIdentifier(tokens[j].text) && !phpModifier(tokens[j].text)) {
		j--
	}
	declared := false
	for ; j >= 0 && phpModifier(tokens[j].text); j-- {
		switch strings.ToLower(tokens[j].text) {
		case "static", "private", "const":
			return false
		case "public", "protected", "var":
			declared = true
		}
	}
	return declared
}

func phpModifier(text string) bool {
	switch strings.ToLower(text) {
	case "public", "protected", "private", "var", "static", "readonly", "const":
		return true
	}
	return false
}

func phpIdentifier(text string) bool {
	if text == "" {
		return false
	}
	c := text[0]
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// phpStatementEnd finds the terminating semicolon outside nested brackets.
func phpStatementEnd(tokens []phpToken, start int) int {
	depth := 0
	for end := start; end < len(tokens); end++ {
		switch tokens[end].text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth == 0 {
				return end
			}
			depth--
		case ";":
			if depth == 0 {
				return end
			}
		}
	}
	return len(tokens)
}

func phpClosingParen(tokens []phpToken, open int) int {
	depth := 0
	for end := open; end < len(tokens); end++ {
		switch tokens[end].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return end
			}
		}
	}
	return len(tokens)
}

func phpString(tokens []phpToken) (string, bool) {
	value, _, ok := phpLocalizable(tokens)
	return value, ok
}

// phpLocalizable also returns the translation source of a $this->l() call: its
// $specific argument, or "" for the module name. Literals have no source.
func phpLocalizable(tokens []phpToken) (value string, source *string, ok bool) {
	if len(tokens) == 1 && strings.EqualFold(tokens[0].text, "null") {
		return "", nil, true
	}
	if len(tokens) == 1 && tokens[0].value != nil {
		return *tokens[0].value, nil, true
	}
	if len(tokens) >= 6 && tokens[0].text == "$this" && tokens[1].text == "->" && strings.EqualFold(tokens[2].text, "l") && tokens[3].text == "(" && tokens[len(tokens)-1].text == ")" {
		args := tokens[4 : len(tokens)-1]
		specific := ""
		if len(args) == 3 && args[1].text == "," && args[2].value != nil {
			specific = *args[2].value
		} else if len(args) != 1 {
			return "", nil, false
		}
		value, literal, ok := phpLocalizable(args[:1])
		if !ok || literal != nil {
			return "", nil, false
		}
		return value, &specific, true
	}
	return "", nil, false
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
