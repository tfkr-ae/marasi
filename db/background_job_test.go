package db

import (
	"errors"
	"testing"
)

// trafficIndexJob returns the saved state of the traffic index build.
func trafficIndexJob(t *testing.T, repo *Repository) JobState {
	t.Helper()
	jobs, err := repo.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for _, job := range jobs {
		if job.Name == TrafficIndexJob {
			return job
		}
	}
	t.Fatalf("\nwanted:\na %s job\ngot:\n%+v", TrafficIndexJob, jobs)
	return JobState{}
}

func TestBackgroundJobs(t *testing.T) {
	t.Run("should list the traffic index as complete on a new project", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		got := trafficIndexJob(t, repo)
		want := JobState{Name: TrafficIndexJob, Complete: true}
		if got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
	})

	t.Run("should reset the traffic index on open when an older binary wrote pairs, then index them", func(t *testing.T) {
		path := t.TempDir() + "/project.marasi"
		repo := openTestProjectFile(t, path)
		ids := legacyPairs(t, repo, "first", "second")
		repo.Close()

		repo = openTestProjectFile(t, path)
		defer repo.Close()
		if indexComplete(t, repo) {
			t.Fatalf("\nwanted:\nincomplete after reopening\ngot:\ncomplete")
		}
		if got := trafficIndexJob(t, repo); got.Complete || got.Done != 0 {
			t.Fatalf("\nwanted:\nan incomplete job with nothing done\ngot:\n%+v", got)
		}

		indexMissing(t, repo, indexAll)

		if got, want := trafficIndexJob(t, repo), (JobState{Name: TrafficIndexJob, Complete: true, Done: 2, Total: 2}); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		if !indexComplete(t, repo) {
			t.Fatalf("\nwanted:\ncomplete index\ngot:\nincomplete")
		}
		if got := listNames(t, repo, ids, `"body-"`); got != "second,first" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "second,first", got)
		}
	})

	t.Run("should resume a build after the project closes mid-way without losing progress", func(t *testing.T) {
		path := t.TempDir() + "/project.marasi"
		repo := openTestProjectFile(t, path)
		ids := sizedPairs(t, repo, 1000, "alpha", "bravo", "charlie", "delta", "echo")
		repo = reopenProject(t, repo)

		indexMissing(t, repo, 2500)
		before := trafficIndexJob(t, repo)
		if want := (JobState{Name: TrafficIndexJob, Done: 2, Total: 5}); before != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, before)
		}

		repo = reopenProject(t, repo)
		if got := trafficIndexJob(t, repo); got != before {
			t.Fatalf("\nwanted:\nthe saved progress %+v after reopening\ngot:\n%+v", before, got)
		}
		for _, want := range []int64{4, 5} {
			indexMissing(t, repo, 2500)
			if got := trafficIndexJob(t, repo).Done; got != want {
				t.Fatalf("\nwanted:\n%d done\ngot:\n%d", want, got)
			}
		}
		if !trafficIndexJob(t, repo).Complete {
			t.Fatalf("\nwanted:\ncomplete\ngot:\nincomplete")
		}
		if got := listNames(t, repo, ids, `"body-"`); got != "echo,delta,charlie,bravo,alpha" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "echo,delta,charlie,bravo,alpha", got)
		}
	})

	t.Run("should rebuild the traffic index from scratch", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		ids := captureTextPairs(t, repo, textSearchPairs())

		if err := repo.RebuildJob(TrafficIndexJob); err != nil {
			t.Fatalf("RebuildJob: %v", err)
		}
		if got := trafficIndexJob(t, repo); got.Complete || got.Done != 0 {
			t.Fatalf("\nwanted:\nan incomplete job with nothing done\ngot:\n%+v", got)
		}
		if got := listNames(t, repo, ids, `"password"`); got != "" {
			t.Fatalf("\nwanted:\nno pair searchable until the rebuild runs\ngot:\n%q", got)
		}

		indexMissing(t, repo, indexAll)

		want := JobState{Name: TrafficIndexJob, Complete: true, Done: int64(len(ids)), Total: int64(len(ids))}
		if got := trafficIndexJob(t, repo); got != want {
			t.Fatalf("\nwanted:\n%+v\ngot:\n%+v", want, got)
		}
		if got := listNames(t, repo, ids, `response_body:"password"`); got != "reset" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "reset", got)
		}
	})

	t.Run("should refuse to rebuild a job it does not know", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()
		if err := repo.RebuildJob("no_such_job"); !errors.Is(err, ErrUnknownJob) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrUnknownJob, err)
		}
	})

	t.Run("should rebuild a job on open when its saved version differs", func(t *testing.T) {
		path := t.TempDir() + "/project.marasi"
		repo := openTestProjectFile(t, path)
		ids := captureTextPairs(t, repo, textSearchPairs())
		// Another binary ran the job under a different rule version.
		if _, err := repo.dbConn.Exec(`UPDATE background_job SET version = version + 1 WHERE name = ?`, TrafficIndexJob); err != nil {
			t.Fatalf("changing the saved version: %v", err)
		}

		repo = reopenProject(t, repo)
		if indexComplete(t, repo) {
			t.Fatalf("\nwanted:\nincomplete index after a version change\ngot:\ncomplete")
		}
		if got := listNames(t, repo, ids, `"password"`); got != "" {
			t.Fatalf("\nwanted:\nthe old index discarded\ngot:\n%q", got)
		}
		indexMissing(t, repo, indexAll)
		if got := listNames(t, repo, ids, `response_body:"password"`); got != "reset" {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", "reset", got)
		}
	})
}
