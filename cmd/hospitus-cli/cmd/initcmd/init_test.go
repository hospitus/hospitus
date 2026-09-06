package initcmd

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/hospitus/hospitus/pkg/doctor"
)

func TestSelectedProviders(t *testing.T) {
	if selectedProviders(nil) != nil {
		t.Error("empty flag should mean all providers (nil set)")
	}
	got := selectedProviders([]string{"Jail", " bhyve "})
	if !got["jail"] || !got["bhyve"] || len(got) != 2 {
		t.Errorf("provider set = %v", got)
	}
}

func TestSymbol(t *testing.T) {
	if symbol(doctor.StatusOK) != "[ OK ]" || symbol(doctor.StatusFail) != "[FAIL]" {
		t.Error("unexpected symbols")
	}
}

func TestFixableFailures(t *testing.T) {
	fixable := doctor.Check{ID: "fixable-fail", Fix: func(context.Context, doctor.System) error { return nil }}
	notFixable := doctor.Check{ID: "plain-fail"}
	rep := doctor.Report{Results: []doctor.CheckResult{
		{Check: fixable, Result: doctor.Result{Status: doctor.StatusFail}},
		{Check: notFixable, Result: doctor.Result{Status: doctor.StatusFail}},
		{Check: doctor.Check{ID: "ok", Fix: func(context.Context, doctor.System) error { return nil }}, Result: doctor.Result{Status: doctor.StatusOK}},
	}}
	got := fixableFailures(rep)
	if len(got) != 1 || got[0].ID != "fixable-fail" {
		t.Errorf("expected only the fixable failing check, got %+v", got)
	}
}

func TestPrintReportAndSummary(t *testing.T) {
	rep := doctor.Report{Results: []doctor.CheckResult{
		{Check: doctor.Check{Name: "ZFS", Required: true}, Result: doctor.Result{Status: doctor.StatusOK, Detail: "pool ok"}},
		{Check: doctor.Check{Name: "DataDir", Required: true}, Result: doctor.Result{Status: doctor.StatusFail, Detail: "missing", Remediation: "mkdir -p X"}},
		{Check: doctor.Check{Name: "PF"}, Result: doctor.Result{Status: doctor.StatusWarn, Detail: "off", Remediation: "line1\nline2"}},
	}}
	var b strings.Builder
	printReport(&b, rep)
	out := b.String()
	if !strings.Contains(out, "[ OK ] ZFS") || !strings.Contains(out, "(required)") {
		t.Errorf("OK line/required missing:\n%s", out)
	}
	if !strings.Contains(out, "[FAIL] DataDir") || !strings.Contains(out, "→ mkdir -p X") {
		t.Errorf("FAIL remediation missing:\n%s", out)
	}
	// multi-line remediation is indented per line
	if !strings.Contains(out, "→ line1") || !strings.Contains(out, "→ line2") {
		t.Errorf("multiline remediation missing:\n%s", out)
	}

	var s strings.Builder
	printSummary(&s, rep)
	if !strings.Contains(s.String(), "1 OK") || !strings.Contains(s.String(), "1 failed") {
		t.Errorf("summary wrong:\n%s", s.String())
	}
}

func TestInteractiveConfirm(t *testing.T) {
	var out strings.Builder
	yes := interactiveConfirm(&out, bufio.NewReader(strings.NewReader("y\n")))
	if !yes(doctor.Check{Name: "x"}) {
		t.Error("'y' should confirm")
	}
	no := interactiveConfirm(&out, bufio.NewReader(strings.NewReader("\n")))
	if no(doctor.Check{Name: "x"}) {
		t.Error("empty (default) should decline")
	}
}

// canned builds a runner whose checks ignore the System and return fixed
// results, so the CLI orchestration can be tested without a real host.
func cannedRunner(checks ...doctor.Check) *doctor.Runner {
	return doctor.NewRunner(nil, checks)
}

func coreCheck(id string, st doctor.Status, required bool, fix func(context.Context, doctor.System) error) doctor.Check {
	return doctor.Check{
		ID: id, Name: id, Required: required, Providers: []string{doctor.ProviderCore},
		Run: func(context.Context, doctor.System) doctor.Result { return doctor.Result{Status: st} },
		Fix: fix,
	}
}

func TestRunInitCheckMode(t *testing.T) {
	noRoot := func() error { return nil }

	// All OK → nil error.
	okRunner := cannedRunner(coreCheck("a", doctor.StatusOK, true, nil))
	var out strings.Builder
	if err := runInitWithRunner(context.Background(), &out, strings.NewReader(""), okRunner, true, false, nil, noRoot); err != nil {
		t.Errorf("all-ok check should return nil, got %v", err)
	}
	if !strings.Contains(out.String(), "Summary:") {
		t.Error("expected a summary")
	}

	// Required failure → non-nil error (drives exit code), report printed.
	failRunner := cannedRunner(coreCheck("b", doctor.StatusFail, true, nil))
	out.Reset()
	if err := runInitWithRunner(context.Background(), &out, strings.NewReader(""), failRunner, true, false, nil, noRoot); err == nil {
		t.Error("required failure should return an error in --check")
	}
}

func TestRunInitAutoFix(t *testing.T) {
	noRoot := func() error { return nil }
	fixed := false
	// A fixable failing check whose Fix flips an internal flag; its Run returns
	// OK once fixed.
	fix := func(context.Context, doctor.System) error { fixed = true; return nil }
	c := doctor.Check{
		ID: "fixme", Name: "fixme", Required: true, Providers: []string{doctor.ProviderCore},
		Run: func(context.Context, doctor.System) doctor.Result {
			if fixed {
				return doctor.Result{Status: doctor.StatusOK}
			}
			return doctor.Result{Status: doctor.StatusFail}
		},
		Fix: fix,
	}
	runner := cannedRunner(c)
	var out strings.Builder
	err := runInitWithRunner(context.Background(), &out, strings.NewReader(""), runner, false, true, nil, noRoot)
	if err != nil {
		t.Errorf("auto-fix should succeed, got %v", err)
	}
	if !fixed {
		t.Error("fix should have been applied in --auto")
	}
	if !strings.Contains(out.String(), "FIXD") && !strings.Contains(out.String(), "Applying fixes") {
		t.Errorf("expected fix output:\n%s", out.String())
	}
}

func TestRunInitFixModeRequiresRoot(t *testing.T) {
	rootErr := func() error { return context.Canceled } // stand-in error
	runner := cannedRunner(coreCheck("x", doctor.StatusFail, true, func(context.Context, doctor.System) error { return nil }))
	var out strings.Builder
	if err := runInitWithRunner(context.Background(), &out, strings.NewReader(""), runner, false, true, nil, rootErr); err == nil {
		t.Error("fix mode must fail when root check fails")
	}
}

func TestRunInitNothingToFix(t *testing.T) {
	noRoot := func() error { return nil }
	// A failing but NON-fixable required check: nothing to fix, but required
	// failure remains → error.
	runner := cannedRunner(coreCheck("hard", doctor.StatusFail, true, nil))
	var out strings.Builder
	if err := runInitWithRunner(context.Background(), &out, strings.NewReader(""), runner, false, false, nil, noRoot); err == nil {
		t.Error("unfixable required failure should return an error")
	}
	if !strings.Contains(out.String(), "Nothing to fix") {
		t.Errorf("expected 'Nothing to fix':\n%s", out.String())
	}
}

func TestSummaryWarningAgreesWithItsCount(t *testing.T) {
	one := doctor.Report{Results: []doctor.CheckResult{
		{Check: doctor.Check{ID: "a"}, Result: doctor.Result{Status: doctor.StatusWarn}},
	}}
	var s strings.Builder
	printSummary(&s, one)
	if !strings.Contains(s.String(), "1 warning,") {
		t.Errorf("one warning should read \"1 warning\":\n%s", s.String())
	}

	two := doctor.Report{Results: []doctor.CheckResult{
		{Check: doctor.Check{ID: "a"}, Result: doctor.Result{Status: doctor.StatusWarn}},
		{Check: doctor.Check{ID: "b"}, Result: doctor.Result{Status: doctor.StatusWarn}},
	}}
	s.Reset()
	printSummary(&s, two)
	if !strings.Contains(s.String(), "2 warnings,") {
		t.Errorf("two warnings should read \"2 warnings\":\n%s", s.String())
	}
}
