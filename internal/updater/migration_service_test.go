package updater

import (
	"reflect"
	"testing"
)

func TestLocalMigrationServiceCommandKeepsDaemonAndStopSemantics(t *testing.T) {
	for _, migration := range []bool{false, true} {
		for _, action := range []string{"stop", "restart"} {
			args, err := agentServiceCommandArgs(action, "fixed-agent.service", migration)
			want := []string{action, "fixed-agent.service"}
			if migration && action == "restart" {
				want = append([]string{"--job-mode=ignore-requirements"}, want...)
			}
			if err != nil || !reflect.DeepEqual(args, want) {
				t.Fatal(migration, action, args, err)
			}
		}
	}
	if _, err := agentServiceCommandArgs("start", "fixed-agent.service", true); err == nil {
		t.Fatal("unsupported action accepted")
	}
}

func TestLocalMigrationRequiresNoUpdaterProcessOrQueuedStart(t *testing.T) {
	for _, load := range []string{"loaded", "not-found"} {
		if err := migrationUpdaterStopped(map[string]string{"LoadState": load, "ActiveState": "inactive", "MainPID": "0", "Job": "0"}); err != nil {
			t.Fatal(load, err)
		}
	}
	for key, value := range map[string]string{"LoadState": "masked", "ActiveState": "activating", "MainPID": "123", "Job": "1", "invalidPID": "not-a-pid"} {
		p := map[string]string{"LoadState": "loaded", "ActiveState": "inactive", "MainPID": "0", "Job": "0"}
		if key == "invalidPID" {
			key = "MainPID"
		}
		p[key] = value
		if migrationUpdaterStopped(p) == nil {
			t.Fatal("unsafe Updater state accepted", key, value)
		}
	}
}

func TestLocalMigrationKeepsStockImplicitDependenciesAndExcludesOnlyWantedUpdater(t *testing.T) {
	p := map[string]string{"Wants": "network-online.target fixed-updater.service", "Requires": "sysinit.target system.slice -.mount var-lib.mount", "Requisite": "", "BindsTo": "", "Conflicts": "shutdown.target"}
	ready, inactive, err := migrationDependencies(p, "fixed-updater.service")
	if err != nil || !reflect.DeepEqual(ready, []string{"network-online.target", "sysinit.target", "system.slice", "-.mount", "var-lib.mount"}) || !reflect.DeepEqual(inactive, []string{"shutdown.target"}) {
		t.Fatal(ready, inactive, err)
	}
	p["Requires"] += " fixed-updater.service"
	if _, _, err := migrationDependencies(p, "fixed-updater.service"); err == nil {
		t.Fatal("mandatory Updater dependency accepted")
	}
	p["Requires"] = "sysinit.target"
	delete(p, "BindsTo")
	if _, _, err := migrationDependencies(p, "fixed-updater.service"); err == nil {
		t.Fatal("incomplete effective dependency properties accepted")
	}
}

func TestLocalMigrationPropertyParserRejectsAmbiguousInput(t *testing.T) {
	for _, input := range []string{"MainPID=0\nMainPID=123", "MainPID", "=0"} {
		if _, err := migrationServiceProperties([]byte(input)); err == nil {
			t.Fatal(input)
		}
	}
}
