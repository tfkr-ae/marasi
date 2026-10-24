package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoteProjects(t *testing.T) {
	binary := buildMarasi(t)
	instance := startRemoteInstance(t, binary)
	projectsDir := filepath.Join(instance.configDir, "projects")
	if err := os.WriteFile(filepath.Join(projectsDir, "alpha.marasi"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(projectsDir, "folder.marasi"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(projectsDir, "alpha.marasi"), filepath.Join(projectsDir, "linked.marasi")); err != nil {
		t.Fatal(err)
	}

	t.Run("should list the instance machine's projects over --remote as the local list does there", func(t *testing.T) {
		for _, args := range [][]string{
			{"project", "list"},
			{"project", "list", "--json"},
		} {
			wantOut, wantErr, wantRunErr := runMarasiEnv(binary, nil, append([]string{"--config-dir", instance.configDir}, args...)...)
			gotOut, gotErr, gotRunErr := instance.remote(args...)
			if gotOut != wantOut || gotErr != wantErr || exitCode(gotRunErr) != exitCode(wantRunErr) {
				t.Fatalf("\nwanted:\n%v: stdout %q stderr %q exit %d\ngot:\nstdout %q stderr %q exit %d", args, wantOut, wantErr, exitCode(wantRunErr), gotOut, gotErr, exitCode(gotRunErr))
			}
		}
	})

	t.Run("should list this machine's projects without --remote and with no instance", func(t *testing.T) {
		stdout, stderr, err := runMarasiEnv(binary, nil, "--config-dir", instance.cliDir, "project", "list", "--json")
		if want := "{\"items\":[]}\n"; err != nil || stdout != want || stderr != "" {
			t.Fatalf("\nwanted:\n%q\ngot:\nstdout %q stderr %q err %v", want, stdout, stderr, err)
		}
	})
}
