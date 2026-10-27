package engine

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// translationFile names module translations that get a config_<iso>.xml. English is
// config.xml; PrestaShop 1.6 language ISO codes have two letters.
var translationFile = regexp.MustCompile(`^translations/([a-z]{2})\.php$`)

// phpTranslations reads the $_MODULE assignments PrestaShop writes to translation
// files, without executing them. Anything else that changes $_MODULE is rejected.
func phpTranslations(source string) (map[string]string, error) {
	tokens := phpTokens(source)
	translations := map[string]string{}
	for i := 0; i < len(tokens); i++ {
		if strings.EqualFold(tokens[i].text, "global") {
			i = phpStatementEnd(tokens, i)
			continue
		}
		if tokens[i].text != "$_MODULE" {
			continue
		}
		end := phpStatementEnd(tokens, i)
		statement := tokens[i:end]
		i = end
		empty := len(statement) == 4 && statement[1].text == "=" && statement[2].text == "[" && statement[3].text == "]" ||
			len(statement) == 5 && statement[1].text == "=" && strings.EqualFold(statement[2].text, "array") && statement[3].text == "(" && statement[4].text == ")"
		switch {
		case len(statement) == 6 && statement[1].text == "[" && statement[2].value != nil && statement[3].text == "]" && statement[4].text == "=" && statement[5].value != nil:
			translations[*statement[2].value] = *statement[5].value
		case empty:
			translations = map[string]string{}
		default:
			return nil, fmt.Errorf("cannot statically read a $_MODULE statement")
		}
	}
	return translations, nil
}

var phpQuotes = regexp.MustCompile(`\\*'`)

// phpModuleTranslation is what Module::l() returned while the module was constructed
// with that back-office language: Translate::getModuleTranslation() for a string
// without sprintf arguments, with no theme override. Untranslated strings fall back
// to the source string.
func phpModuleTranslation(translations map[string]string, module, source, value string) string {
	key := strings.ToLower("<{"+module+"}prestashop>"+source) + "_" + phpModuleKey(value)
	// PHP's empty() also treats "0" as missing.
	if translated, ok := translations[key]; ok && translated != "" && translated != "0" {
		value = translated
	} else {
		value = phpQuotes.ReplaceAllString(value, `\'`)
	}
	return phpHTMLSpecialChars(phpStripSlashes(value))
}

// phpModuleKey is the md5 suffix of a translation key; apostrophes count as \'.
func phpModuleKey(value string) string {
	sum := md5.Sum([]byte(phpQuotes.ReplaceAllString(value, `\'`)))
	return hex.EncodeToString(sum[:])
}

func phpStripSlashes(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			out.WriteByte(value[i])
			continue
		}
		i++
		if i == len(value) {
			break
		}
		if value[i] == '0' {
			out.WriteByte(0)
		} else {
			out.WriteByte(value[i])
		}
	}
	return out.String()
}

// phpHTMLSpecialChars is htmlspecialchars($value, ENT_COMPAT, 'UTF-8').
var phpHTMLSpecialChars = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace
