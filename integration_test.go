package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/brandnewteam/presto/internal/engine"
	"github.com/brandnewteam/presto/internal/testshop"
)

func TestCLIEndToEnd(t *testing.T) {
	workspace := t.TempDir()
	binary := filepath.Join(workspace, "presto agent's binary")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	project := filepath.Join(workspace, "checkout with spaces")
	put := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(project, "examplemodule.php"), "<?php\n$this->version = '1.2.3';\n")
	put(filepath.Join(project, "presto.toml"), "schema=1\n[module]\nname='examplemodule'\n")
	put(filepath.Join(project, "views", "template.tpl"), "new template")
	put(filepath.Join(project, "uploads", "local.txt"), "ignore")
	localShop := filepath.Join(workspace, "local shop")
	remoteShop := filepath.Join(workspace, "remote shop's root")
	for _, shop := range []string{localShop, remoteShop} {
		put(filepath.Join(shop, "config", "config.inc.php"), "<?php")
		put(filepath.Join(shop, "modules", "examplemodule", "obsolete.php"), "remove")
		put(filepath.Join(shop, "modules", "examplemodule", "uploads", "live.txt"), "runtime")
	}
	put(filepath.Join(remoteShop, "modules", "examplemodule", "views", "template.tpl"), "old template")
	global := filepath.Join(workspace, "config.toml")
	put(global, "schema=1\n[targets.local]\ntype='local'\nroot="+strconv.Quote(localShop)+"\n[targets.remote]\ntype='ssh'\nhost='fixture.invalid'\nroot="+strconv.Quote(remoteShop)+"\nagent="+strconv.Quote(binary)+"\n")
	environment := append(os.Environ(), "PRESTO_CONFIG="+global)
	var logs string // stderr of the last invocation
	invoke := func(args ...string) ([]byte, error) {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Dir = project
		command.Env = environment
		var stderr bytes.Buffer
		command.Stderr = &stderr
		out, err := command.Output()
		logs = stderr.String()
		if err != nil {
			return out, &commandError{err, logs}
		}
		return out, nil
	}
	if out, err := invoke("targets", "--json"); err != nil || !bytes.Contains(out, []byte(`"local"`)) {
		t.Fatalf("targets: %s %v", out, err)
	}
	if _, err := invoke("deploy", "local"); err == nil {
		t.Fatal("deploy accepted local target")
	}
	if _, err := invoke("sync", "remote"); err == nil {
		t.Fatal("sync accepted SSH target")
	}
	out, err := invoke("build", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var built struct {
		Archive string `json:"archive"`
	}
	if err := json.Unmarshal(out, &built); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(built.Archive)
	if err != nil {
		t.Fatal(err)
	}
	foundConfigXML := false
	for _, file := range archive.File {
		if file.Name == "examplemodule/config.xml" {
			foundConfigXML = true
		}
		if strings.Contains(file.Name, "presto.toml") || strings.Contains(file.Name, "uploads") {
			t.Fatal("packaged config or runtime data")
		}
	}
	archive.Close()
	if !foundConfigXML {
		t.Fatal("distribution archive is missing config.xml")
	}
	metadata, err := os.Open(filepath.Join(project, "config.xml"))
	if err == nil {
		metadata.Close()
		t.Fatal("build changed the source checkout")
	}
	out, err = invoke("sync", "local", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var planned engine.Result
	if err := json.Unmarshal(out, &planned); err != nil {
		t.Fatal(err)
	}
	if !planned.DryRun || len(planned.Deleted) != 1 {
		t.Fatalf("unexpected dry run: %s", out)
	}
	if _, err := os.Stat(filepath.Join(localShop, "modules", "examplemodule", "obsolete.php")); err != nil {
		t.Fatal("dry run removed file")
	}
	out, err = invoke("sync", "local", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var applied engine.Result
	if err := json.Unmarshal(out, &applied); err != nil {
		t.Fatal(err)
	}
	if len(applied.Created) != 2 || len(applied.Deleted) != 1 {
		t.Fatalf("unexpected sync: %s", out)
	}
	if runtime.GOOS == "windows" {
		t.Log("SSH stand-in requires a POSIX shell; native build/local sync exercised above")
		return
	}
	stub := filepath.Join(workspace, "ssh-bin", "ssh")
	put(stub, "#!/bin/sh\nfor arg do last=$arg; done\nexec /bin/sh -c \"$last\"\n")
	if err := os.Chmod(stub, 0755); err != nil {
		t.Fatal(err)
	}
	environment = append(environment, "PATH="+filepath.Dir(stub)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := invoke("doctor", "remote"); err != nil {
		t.Fatal(err)
	}
	out, err = invoke("deploy", "remote", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &planned); err != nil {
		t.Fatal(err)
	}
	if !planned.DryRun || planned.Diffs["views/template.tpl"] != "--- a/views/template.tpl\n+++ b/views/template.tpl\n@@ -1 +1 @@\n-old template\n\\ No newline at end of file\n+new template\n\\ No newline at end of file\n" {
		t.Fatalf("remote dry run not reported with diffs: %s", out)
	}
	if strings.Contains(logs, "too old") {
		t.Fatal(logs)
	}
	// A 0.2.0 receiver lacks the diff capability and rejects the --diff flag.
	put(stub, "#!/bin/sh\nfor arg do last=$arg; done\ncase \"$last\" in\n*agent-info*) printf '%s\\n' '{\"protocol\":1,\"version\":\"0.2.0\",\"os\":\"linux\",\"arch\":\"amd64\",\"capabilities\":[\"prestashop16-upgrade-v1\"]}' ;;\n*\"'--diff'\"*) echo 'unknown flag: --diff' >&2; exit 1 ;;\n*) exec /bin/sh -c \"$last\" ;;\nesac\n")
	out, err = invoke("deploy", "remote", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var old engine.Result
	if err := json.Unmarshal(out, &old); err != nil || !old.DryRun || len(old.Updated) != 1 || old.Diffs != nil {
		t.Fatalf("old receiver dry run: %s %v", out, err)
	}
	if !strings.Contains(logs, "receiver 0.2.0 too old to show diffs") {
		t.Fatalf("no fallback notice: %s", logs)
	}
	put(stub, "#!/bin/sh\nfor arg do last=$arg; done\nexec /bin/sh -c \"$last\"\n")
	if _, err := os.Stat(filepath.Join(remoteShop, "modules", "examplemodule", "obsolete.php")); err != nil {
		t.Fatal("remote dry run removed file")
	}
	out, err = invoke("deploy", "remote", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &applied); err != nil {
		t.Fatal(err)
	}
	if len(applied.Created) != 2 || len(applied.Updated) != 1 || len(applied.Deleted) != 1 || applied.Diffs != nil {
		t.Fatalf("unexpected deployment: %s", out)
	}
	if _, err := os.Stat(filepath.Join(remoteShop, "modules", "examplemodule", "config.xml")); err != nil {
		t.Fatal("deploy did not generate config.xml:", err)
	}
	if _, err := os.Stat(filepath.Join(localShop, "modules", "examplemodule", "config.xml")); !os.IsNotExist(err) {
		t.Fatal("sync generated config.xml:", err)
	}
	data, err := os.ReadFile(filepath.Join(remoteShop, "modules", "examplemodule", "uploads", "live.txt"))
	if err != nil || string(data) != "runtime" {
		t.Fatalf("lost runtime data: %s %v", data, err)
	}
	put(filepath.Join(workspace, "ssh-bin", "ssh"), "#!/bin/sh\nexit 42\n")
	if err := os.Chmod(stub, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke("deploy", "remote"); err == nil {
		t.Fatal("ignored SSH failure")
	}
}

type commandError struct {
	err  error
	logs string
}

func (e *commandError) Error() string { return e.err.Error() + ": " + e.logs }

func TestCLILifecycleForSyncAndDeploy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX SSH stand-in; engine lifecycle tests remain portable")
	}
	php := testshop.PHP(t)
	base := t.TempDir()
	binary := filepath.Join(base, "presto receiver's binary")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	project := filepath.Join(base, "examplemodule")
	testshop.Put(t, project, "examplemodule.php", testshop.Module("1.3.0", "if (!empty(prestoState()['fail_constructor'])) { throw new Exception('bad constructor'); } echo 'module output';"))
	testshop.Put(t, project, "upgrade/upgrade-1.1.0.php", "<?php\nfunction upgrade_module_1_1_0($module) { echo 'migration output'; return true; }\n")
	local, remote := testshop.Create(t), testshop.Create(t)
	testshop.Set(t, remote, "fail_constructor", true)
	phpJSON, _ := json.Marshal(php) // JSON string arrays are also valid TOML arrays here.
	global := filepath.Join(base, "config.toml")
	settings := "\nupgrade=true\nshop_context='group'\nshop_group_id=1\nphp=" + string(phpJSON) + "\n"
	testshop.Put(t, base, "config.toml", "[targets.local]\ntype='local'\nroot="+strconv.Quote(local)+"\n[targets.local.prestashop]"+settings+"[targets.remote]\ntype='ssh'\nhost='fixture.invalid'\nroot="+strconv.Quote(remote)+"\nagent="+strconv.Quote(binary)+"\n[targets.remote.prestashop]"+settings)
	stub := filepath.Join(base, "ssh-bin", "ssh")
	testshop.Put(t, base, "ssh-bin/ssh", "#!/bin/sh\nfor arg do last=$arg; done\nexec /bin/sh -c \"$last\"\n")
	if err := os.Chmod(stub, 0755); err != nil {
		t.Fatal(err)
	}
	environment := append(os.Environ(), "PRESTO_CONFIG="+global, "PATH="+filepath.Dir(stub)+string(os.PathListSeparator)+os.Getenv("PATH"))
	invoke := func(args ...string) ([]byte, string, error) {
		command := exec.Command(binary, args...)
		command.Dir = project
		command.Env = environment
		var logs bytes.Buffer
		command.Stderr = &logs
		out, err := command.Output()
		return out, logs.String(), err
	}
	out, logs, err := invoke("sync", "local", "--json")
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, logs)
	}
	var result engine.Result
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("corrupt JSON due to PHP output: %v %s", err, out)
	}
	if result.Upgrade == nil || result.Upgrade.Status != "upgraded" || result.Upgrade.ToVersion != "1.3.0" {
		t.Fatalf("%s", out)
	}
	before := testshop.State(t, remote)
	out, logs, err = invoke("deploy", "remote", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("dry run: %v %s", err, logs)
	}
	if err := json.Unmarshal(out, &result); err != nil || result.Upgrade.Status != "planned" {
		t.Fatalf("%s %v", out, err)
	}
	after := testshop.State(t, remote)
	if before["version"] != after["version"] {
		t.Fatal("remote dry run upgraded database")
	}
	out, logs, err = invoke("deploy", "remote", "--json")
	if err == nil {
		t.Fatal("reported successful deployment after constructor error")
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("missing structured failure: %v %s\n%s", err, out, logs)
	}
	if result.Error == "" || result.Upgrade == nil || !result.Upgrade.Disabled || result.Upgrade.Status != "failed" {
		t.Fatalf("failure lost over SSH: %s\n%s", out, logs)
	}
	enabled := testshop.State(t, remote)["enabled"].([]any)
	if len(enabled) != 1 || enabled[0] != float64(3) {
		t.Fatalf("disabled shops outside group: %v", enabled)
	}
	testshop.Put(t, base, "ssh-bin/ssh", "#!/bin/sh\nprintf '%s' '{\"protocol\":1,\"version\":\"old\",\"os\":\"linux\",\"arch\":\"amd64\"}'\n")
	if err := os.Chmod(stub, 0755); err != nil {
		t.Fatal(err)
	}
	_, logs, err = invoke("deploy", "remote", "--json")
	if err == nil || !strings.Contains(logs, "does not support PrestaShop 1.6 finalization") {
		t.Fatalf("accepted incompatible receiver: %v %s", err, logs)
	}
}
