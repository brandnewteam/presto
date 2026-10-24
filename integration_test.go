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

	"presto/internal/engine"
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
	global := filepath.Join(workspace, "config.toml")
	put(global, "schema=1\n[targets.local]\ntype='local'\nroot="+strconv.Quote(localShop)+"\n[targets.remote]\ntype='ssh'\nhost='fixture.invalid'\nroot="+strconv.Quote(remoteShop)+"\nagent="+strconv.Quote(binary)+"\n")
	environment := append(os.Environ(), "PRESTO_CONFIG="+global)
	invoke := func(args ...string) ([]byte, error) {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Dir = project
		command.Env = environment
		var logs bytes.Buffer
		command.Stderr = &logs
		out, err := command.Output()
		if err != nil {
			return out, &commandError{err, string(logs.Bytes())}
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
	for _, file := range archive.File {
		if strings.Contains(file.Name, "presto.toml") || strings.Contains(file.Name, "uploads") {
			t.Fatal("packaged config or runtime data")
		}
	}
	archive.Close()
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
	if !planned.DryRun {
		t.Fatal("remote dry run not reported")
	}
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
	if len(applied.Created) != 2 || len(applied.Deleted) != 1 {
		t.Fatalf("unexpected deployment: %s", out)
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
