package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/brandnewteam/presto/internal/config"
)

const translatedModule = `<?php
class Examplemodule extends Module {
    public function __construct() {
        $this->version = '1.2.3';
        $this->displayName = $this->l('Modulo d\'esempio');
        $this->description = $this->l('Shows "quotes" & <tags>', 'helper');
        $this->author = 'Brand New & Co.';
        $this->tab = 'administration';
        $this->confirmUninstall = $this->l('Back\\slash it\'s untranslated');
        $this->need_instance = 0;
    }
    public function getContent() {}
}`

// Written like PrestaShop's AdminTranslations; keys use md5 of the string with \'.
const italianTranslations = `<?php

global $_MODULE;
$_MODULE = array();
$_MODULE['<{examplemodule}prestashop>examplemodule_ignored'] = 'Not a key PrestaShop looks up';
$_MODULE['<{examplemodule}prestashop>examplemodule_` + "KEY1" + `'] = 'Modulo d\'esempio: più & "meglio"';
$_MODULE['<{examplemodule}prestashop>helper_` + "KEY2" + `'] = 'Mostra <b>tag</b> € — ok';
`

func TestPHPTranslationsReadsAssignmentsOnly(t *testing.T) {
	got, err := phpTranslations("<?php\nglobal $_MODULES, $_MODULE;\n$_MODULE = array();\n$_MODULE['<{m}prestashop>m_a'] = 'L\\'ordine';\n$_MODULE[\"<{m}prestashop>m_b\"] = \"Tab\\there\";\n# $_MODULE['x'] = 'comment';\n")
	want := map[string]string{"<{m}prestashop>m_a": "L'ordine", "<{m}prestashop>m_b": "Tab\there"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := phpTranslations("<?php $_MODULE['a'] = 'x'; $_MODULE = [];"); err != nil || len(got) != 0 {
		t.Fatalf("reset ignored: %q %v", got, err)
	}
	for _, source := range []string{
		"<?php $_MODULE['a'] = 'x' . 'y';",
		"<?php $_MODULE['a'] = \"$name\";",
		"<?php $_MODULE = include 'other.php';",
		"<?php $_MODULE['a'] = __('x');",
	} {
		if _, err := phpTranslations(source); err == nil {
			t.Errorf("accepted %s", source)
		}
	}
}

func TestPHPModuleTranslationMatchesTranslate(t *testing.T) {
	translations := map[string]string{
		"<{m}prestashop>m_" + phpModuleKey("Don't") + "":  `Non \"farlo\"`,
		"<{m}prestashop>m_" + phpModuleKey("Zero"):        "0",
		"<{m}prestashop>other_" + phpModuleKey("Scoped"):  "Ambito",
		"<{m}prestashop>m_" + phpModuleKey("Ampersand &"): "E & <b>",
	}
	for _, tc := range []struct{ source, value, want string }{
		{"m", "Don't", "Non &quot;farlo&quot;"},
		{"m", "Zero", "Zero"},         // empty("0") falls back
		{"Other", "Scoped", "Ambito"}, // source is lowercased
		{"m", "Scoped", "Scoped"},     // wrong source
		{"m", "Ampersand &", "E &amp; &lt;b&gt;"},
		{"m", `a\b it's`, "ab it's"}, // stripslashes on the fallback
	} {
		if got := phpModuleTranslation(translations, "m", tc.source, tc.value); got != tc.want {
			t.Errorf("%s %q: got %q, want %q", tc.source, tc.value, got, tc.want)
		}
	}
}

func italianFixture() string {
	return strings.NewReplacer("KEY1", phpModuleKey("Modulo d'esempio"), "KEY2", phpModuleKey(`Shows "quotes" & <tags>`)).Replace(italianTranslations)
}

// The PHP side runs Translate::getModuleTranslation() from PrestaShop 1.6.1 on the
// constructor's $this->l() calls, then the native _generateConfigXml() expression.
func TestTranslatedConfigXMLIsByteIdenticalToNativeGenerator(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("PHP is not installed")
	}
	translations, err := phpTranslations(italianFixture())
	if err != nil {
		t.Fatal(err)
	}
	data, err := renderConfigXML(&Snapshot{Name: "examplemodule", Version: "1.2.3"}, config.ModuleXML{}, translatedModule, translations)
	if err != nil {
		t.Fatal(err)
	}
	translate := `$_MODULE = array();
eval('?>' . base64_decode($argv[1]));
function l($string, $source) {
    global $_MODULE;
    $string = preg_replace("/\\\*'/", "\'", $string);
    $key = md5($string);
    $default_key = strtolower('<{examplemodule}prestashop>'.$source).'_'.$key;
    $ret = !empty($_MODULE[$default_key]) ? stripslashes($_MODULE[$default_key]) : stripslashes($string);
    return htmlspecialchars($ret, ENT_COMPAT, 'UTF-8');
}
$in = json_decode(stream_get_contents(STDIN));
$in->displayName = l($in->displayName, 'examplemodule');
$in->description = l($in->description, 'helper');
$in->confirmUninstall = l($in->confirmUninstall, 'examplemodule');
$stdin = json_encode($in);
`
	native := strings.Replace(nativeConfigXML, "json_decode(stream_get_contents(STDIN))", "json_decode($stdin)", 1)
	metadata, _ := json.Marshal(map[string]any{"name": "examplemodule", "version": "1.2.3", "displayName": "Modulo d'esempio", "description": `Shows "quotes" & <tags>`, "author": "Brand New & Co.", "tab": "administration", "confirmUninstall": `Back\slash it's untranslated`, "is_configurable": 1, "need_instance": 0})
	cmd := exec.Command(php, "-r", translate+native, "--", base64.StdEncoding.EncodeToString([]byte(italianFixture())))
	cmd.Stdin = bytes.NewReader(metadata)
	want, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, want)
	}
	// PrestaShop 1.6 always writes limited_countries; the native expression omits it when unset.
	want = bytes.Replace(want, []byte("</need_instance>\n</module>"), []byte("</need_instance>\n\t<limited_countries></limited_countries>\n</module>"), 1)
	if !bytes.Equal(data, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", data, want)
	}
	for _, fragment := range []string{"Modulo d&#039;esempio: pi&ugrave; &amp; &quot;meglio&quot;", "Mostra &lt;b&gt;tag&lt;/b&gt; &euro; &mdash; ok", "<confirmUninstall><![CDATA[Backslash it's untranslated]]>"} {
		if !bytes.Contains(data, []byte(fragment)) {
			t.Errorf("missing %q in\n%s", fragment, data)
		}
	}
}

func TestAddConfigXMLGeneratesShippedLanguages(t *testing.T) {
	source := moduleFixture(t)
	put(t, source, "examplemodule.php", translatedModule)
	put(t, source, "translations/it.php", italianFixture())
	put(t, source, "translations/fr.php", "<?php\nglobal $_MODULE;\n$_MODULE = array();\n")
	put(t, source, "translations/en.php", "<?php\n$_MODULE = array();\n")
	put(t, source, "translations/index.php", "<?php\nheader('Location: ../');\n")
	put(t, source, "config_it.xml", "stale cache committed from a shop")
	snapshot := prepareFixture(t, source, config.Defaults())
	if err := AddConfigXML(snapshot, config.ModuleXML{}); err != nil {
		t.Fatal(err)
	}
	var generated []string
	for _, file := range snapshot.Files {
		if strings.HasPrefix(file.Path, "config") {
			generated = append(generated, file.Path)
		}
	}
	if !reflect.DeepEqual(generated, []string{"config.xml", "config_fr.xml", "config_it.xml"}) {
		t.Fatal(generated)
	}
	english, french, italian := read(t, snapshot.Root, "config.xml"), read(t, snapshot.Root, "config_fr.xml"), read(t, snapshot.Root, "config_it.xml")
	if !strings.Contains(english, "<displayName><![CDATA[Modulo d&#039;esempio]]>") || !strings.Contains(italian, "pi&ugrave;") {
		t.Fatalf("%s\n%s", english, italian)
	}
	// Untranslated strings still pass through $this->l()'s htmlspecialchars.
	if !strings.Contains(french, "<description><![CDATA[Shows &quot;quotes&quot; &amp; &lt;tags&gt;]]>") {
		t.Fatal(french)
	}
	var wire bytes.Buffer
	if err := WriteArchive(context.Background(), snapshot, &wire, true); err != nil {
		t.Fatal(err)
	}
	shop := shopFixture(t)
	put(t, filepath.Join(shop, "modules", "examplemodule"), "config_de.xml", "language the repository does not ship")
	result, err := Receive(context.Background(), bytes.NewReader(wire.Bytes()), shop, false)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, shop, "modules/examplemodule/config_it.xml") != italian || !reflect.DeepEqual(result.Deleted, []string{"config_de.xml"}) {
		t.Fatalf("%+v", result)
	}
	if read(t, source, "config_it.xml") != "stale cache committed from a shop" {
		t.Fatal("changed the source checkout")
	}
}

func TestAddConfigXMLLanguageErrors(t *testing.T) {
	for name, change := range map[string]func(*Snapshot){
		"preserved": func(s *Snapshot) { s.Rules.Preserve = append(s.Rules.Preserve, "config_it.xml") },
		"case":      func(s *Snapshot) { s.Files = append(s.Files, File{Path: "Config_IT.xml"}) },
		"dynamic":   func(s *Snapshot) { put(t, s.Root, "translations/it.php", "<?php $_MODULE['k'] = sprintf('%s', 'x');") },
	} {
		t.Run(name, func(t *testing.T) {
			source := moduleFixture(t)
			put(t, source, "translations/it.php", "<?php\n$_MODULE = array();\n")
			snapshot := prepareFixture(t, source, config.Defaults())
			change(snapshot)
			if err := AddConfigXML(snapshot, config.ModuleXML{}); err == nil || !strings.Contains(err.Error(), "config_it.xml") {
				t.Fatal(err)
			}
		})
	}
}
