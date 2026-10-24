package engine

import (
	"bytes"
	"context"
	"encoding/xml"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/brandnewteam/presto/internal/config"
)

func TestConfigXMLMatchesNativeMetadata(t *testing.T) {
	source := `<?php
class Examplemodule extends Module {
    public $need_instance = 1;
    public function __construct() {
        // $this->author = 'a misleading comment';
        $this->name = 'examplemodule';
        $this->version = '1.2.3';
        $this->displayName = $this->l('A <shop> & café');
        $this->description = $this->l('A store\'s "description"', 'examplemodule');
        $this->author = "Example author";
        $this->author_uri = 'https://example.com/?a=1&b=2';
        $this->tab = 'front_office_features';
        $this->confirmUninstall = $this->l('Really remove it?');
        $this->need_instance = 0;
        $this->limited_countries = array('FR');
    }
    public function getContent() { return 'configuration'; }
}
class Helper { public function __construct() { $this->author = 'wrong class'; } }
`
	data, err := renderConfigXML(&Snapshot{Name: "examplemodule", Version: "1.2.3"}, config.ModuleXML{}, source)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name         string `xml:"name"`
		DisplayName  string `xml:"displayName"`
		Version      string `xml:"version"`
		Description  string `xml:"description"`
		Author       string `xml:"author"`
		URI          string `xml:"author_uri"`
		Tab          string `xml:"tab"`
		Confirm      string `xml:"confirmUninstall"`
		Configurable int    `xml:"is_configurable"`
		NeedInstance int    `xml:"need_instance"`
		Country      string `xml:"limited_countries"`
	}
	if err := xml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "examplemodule" || got.Version != "1.2.3" || html.UnescapeString(got.DisplayName) != "A <shop> & café" || html.UnescapeString(got.Description) != `A store's "description"` || got.Author != "Example author" || html.UnescapeString(got.URI) != "https://example.com/?a=1&b=2" || got.Tab != "front_office_features" || got.Configurable != 1 || got.NeedInstance != 0 || got.Country != "FR" || got.Confirm != "Really remove it?" {
		t.Fatalf("%+v\n%s", got, data)
	}
	// Exercise the parser used by PrestaShop, not only Go's XML decoder.
	if php, err := exec.LookPath("php"); err == nil {
		cmd := exec.Command(php, "-r", `$xml=simplexml_load_string(stream_get_contents(STDIN)); if (!$xml || (string)$xml->name !== 'examplemodule' || html_entity_decode((string)$xml->displayName, ENT_QUOTES, 'UTF-8') !== hex2bin('41203c73686f703e202620636166c3a9') || (int)$xml->need_instance !== 0) {exit(1);}`)
		cmd.Stdin = bytes.NewReader(data)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native XML parser: %v %s", err, out)
		}
	}
}

func TestConfigXMLDynamicMetadataAndOverrides(t *testing.T) {
	source := `<?php class examplemodule extends Module { public function __construct() { $this->displayName = getenv('TITLE'); } }`
	snapshot := &Snapshot{Name: "examplemodule", Version: "1.2.3"}
	if _, err := renderConfigXML(snapshot, config.ModuleXML{}, source); err == nil || !strings.Contains(err.Error(), "module.config_xml.display_name") {
		t.Fatalf("%v", err)
	}
	title := "A ]]> title & café"
	no := false
	countries := []string{"FR", "IT"}
	settings := config.ModuleXML{DisplayName: &title, NeedInstance: &no, LimitedCountries: &countries}
	data, err := renderConfigXML(snapshot, settings, source)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Title   string `xml:"displayName"`
		Need    int    `xml:"need_instance"`
		Country string `xml:"limited_countries"`
	}
	if err := xml.Unmarshal(data, &got); err != nil || html.UnescapeString(got.Title) != title || got.Need != 0 || got.Country != "" {
		t.Fatalf("%+v %v\n%s", got, err, data)
	}
	for _, source := range []string{
		`<?php $this->displayName = "Hello $name";`,
		`<?php $this->displayName = 'first'; $this->displayName = 'second';`,
		`<?php if (true) { $this->displayName = 'conditional'; }`,
		`<?php if (true) $this->displayName = 'conditional';`,
	} {
		if _, err := renderConfigXML(snapshot, config.ModuleXML{}, source); err == nil {
			t.Fatalf("accepted dynamic metadata: %s", source)
		}
	}
	bad := "invalid\x00text"
	if _, err := renderConfigXML(snapshot, config.ModuleXML{Description: &bad}, ""); err == nil {
		t.Fatal("accepted XML control character")
	}
	if _, err := renderConfigXML(&Snapshot{Name: "examplemodule"}, settings, source); err == nil {
		t.Fatal("invented an unknown version")
	}
}

func TestGeneratedConfigXMLIsIsolatedAndTransported(t *testing.T) {
	source := moduleFixture(t)
	put(t, source, "config.xml", "stale source cache")
	snapshot := prepareFixture(t, source, config.Defaults())
	if snapshot.Rules.Selected("config.xml") {
		t.Fatal("sync default changed")
	}
	if err := AddConfigXML(snapshot, config.ModuleXML{}); err != nil {
		t.Fatal(err)
	}
	first := read(t, snapshot.Root, "config.xml")
	if err := AddConfigXML(snapshot, config.ModuleXML{}); err != nil {
		t.Fatal(err)
	}
	if read(t, snapshot.Root, "config.xml") != first || read(t, source, "config.xml") != "stale source cache" {
		t.Fatal("generation is not isolated and deterministic")
	}
	count := 0
	for _, file := range snapshot.Files {
		if file.Path == "config.xml" {
			count++
		}
	}
	if count != 1 || !snapshot.Rules.Selected("config.xml") {
		t.Fatalf("invalid generated manifest: %+v", snapshot)
	}
	var wire bytes.Buffer
	if err := WriteArchive(context.Background(), snapshot, &wire, true); err != nil {
		t.Fatal(err)
	}
	shop := shopFixture(t)
	result, err := Receive(context.Background(), bytes.NewReader(wire.Bytes()), shop, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Created, []string{"config.xml", "examplemodule.php", "src/code.php"}) {
		t.Fatal(result)
	}
	if read(t, shop, "modules/examplemodule/config.xml") != first {
		t.Fatal("receiver lost metadata")
	}
	// A separately prepared sync must keep its original source selection.
	syncSnapshot, cleanup, err := Prepare(context.Background(), source, config.Defaults(), false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, file := range syncSnapshot.Files {
		if file.Path == "config.xml" {
			t.Fatal("sync generates config.xml")
		}
	}
}

func TestConfigXMLRespectsPreservationAndRejectsConflicts(t *testing.T) {
	for _, kind := range []string{"preserved", "case", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "symlink" && runtime.GOOS == "windows" {
				t.Skip("symlink privileges")
			}
			snapshot := prepareFixture(t, moduleFixture(t), config.Defaults())
			switch kind {
			case "preserved":
				snapshot.Rules.Preserve = append(snapshot.Rules.Preserve, "config.xml")
			case "case":
				snapshot.Files = append(snapshot.Files, File{Path: "Config.xml"})
			case "directory":
				if err := os.Mkdir(filepath.Join(snapshot.Root, "config.xml"), 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(t.TempDir(), "external.xml"), filepath.Join(snapshot.Root, "config.xml")); err != nil {
					t.Fatal(err)
				}
			}
			if err := AddConfigXML(snapshot, config.ModuleXML{}); err == nil {
				t.Fatal("accepted " + kind)
			}
		})
	}
}
