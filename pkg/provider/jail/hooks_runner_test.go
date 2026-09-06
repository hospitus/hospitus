package jail

import (
	"context"
	"errors"
	"testing"

	"github.com/hospitus/hospitus/pkg/provider"
	"github.com/hospitus/hospitus/pkg/provider/execx"
)

func TestGetHookEnvForJail(t *testing.T) {
	stateDir := t.TempDir()
	p := &JailProvider{stateDir: stateDir, zfsParent: "zroot/hospitus/jails"}
	cfg := &jailConfig{
		Name:     "web",
		Path:     "/zroot/hospitus/jails/web/root",
		Networks: []provider.NetworkSpec{{IPv4: "10.0.0.5", Bridge: "hospitus0"}},
	}
	if err := p.saveJailConfig(cfg, stateDir+"/web.json"); err != nil {
		t.Fatal(err)
	}
	env, err := p.GetHookEnvForJail("web", HookPostStart)
	if err != nil {
		t.Fatalf("GetHookEnvForJail err = %v", err)
	}
	if env.JailPath != "/zroot/hospitus/jails/web/root" {
		t.Errorf("JailPath = %q", env.JailPath)
	}
	if env.ZFSDataset != "zroot/hospitus/jails/web" {
		t.Errorf("ZFSDataset = %q", env.ZFSDataset)
	}
	if env.JailIP != "10.0.0.5" || env.JailBridge != "hospitus0" {
		t.Errorf("network env = %q/%q", env.JailIP, env.JailBridge)
	}
}

func TestExecuteHooksNoHooks(t *testing.T) {
	// jls fails -> not running; no hook dirs, no config hooks -> empty result.
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("stopped") }}
	p := &JailProvider{stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails", runner: fake}
	results, err := p.ExecuteHooks(context.Background(), HookPostStart, HookEnv{JailName: "web"})
	if err != nil {
		t.Fatalf("ExecuteHooks err = %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no hook results, got %d", len(results))
	}
}

func TestCreateListDeleteHookScript(t *testing.T) {
	p := &JailProvider{stateDir: t.TempDir()}
	if err := p.CreateHookScript("web", HookPostStart, "echo hi"); err != nil {
		t.Fatalf("CreateHookScript err = %v", err)
	}
	scripts, err := p.ListHookScripts("web")
	if err != nil {
		t.Fatalf("ListHookScripts err = %v", err)
	}
	if _, ok := scripts[HookPostStart]; !ok {
		t.Errorf("expected post-start hook listed, got %+v", scripts)
	}
	if err := p.DeleteHookScript("web", HookPostStart); err != nil {
		t.Fatalf("DeleteHookScript err = %v", err)
	}
	scripts, _ = p.ListHookScripts("web")
	if len(scripts) != 0 {
		t.Errorf("expected no hooks after delete, got %d", len(scripts))
	}
}

func TestCreateHookScriptInvalidType(t *testing.T) {
	p := &JailProvider{stateDir: t.TempDir()}
	if err := p.CreateHookScript("web", HookType("bogus"), "x"); err == nil {
		t.Fatal("expected error for invalid hook type")
	}
}

func TestExecuteHooksRunsScript(t *testing.T) {
	fake := &execx.Fake{Func: func(_ string, _ []string) ([]byte, error) { return nil, errors.New("stopped") }}
	p := &JailProvider{stateDir: t.TempDir(), zfsParent: "zroot/hospitus/jails", runner: fake}
	if err := p.CreateHookScript("web", HookPostStart, "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatal(err)
	}
	results, err := p.ExecuteHooks(context.Background(), HookPostStart, HookEnv{JailName: "web", HookType: HookPostStart})
	if err != nil {
		t.Fatalf("ExecuteHooks err = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected the discovered hook script to run")
	}
	if results[0].Error != nil {
		t.Errorf("hook script should have succeeded, got %v", results[0].Error)
	}
}
