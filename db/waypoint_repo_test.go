package db

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

func TestWaypointRepo_GetWaypoints(t *testing.T) {
	t.Run("should return an empty waypoint slice if there are none configured", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		waypoints, err := repo.GetWaypoints()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(waypoints) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(waypoints))
		}
	})

	t.Run("should return all the waypoints that are configured", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		want := []*domain.Waypoint{
			{Hostname: "api.marasi.app:80", Override: "127.0.0.1:9000"},
			{Hostname: "marasi.app:443", Override: "127.0.0.1:8080"},
		}

		for index := len(want) - 1; index >= 0; index-- {
			waypoint := want[index]
			err := repo.CreateOrUpdateWaypoint(waypoint.Hostname, waypoint.Override)
			if err != nil {
				t.Fatalf("creating waypoints : %v", err)
			}
		}

		got, err := repo.GetWaypoints()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 2 {
			t.Fatalf("\nwanted:\n2\ngot:\n%d", len(got))
		}

		if !reflect.DeepEqual(want, got) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", want, got)
		}
	})
}

func TestWaypointRepo_CreateWaypoint(t *testing.T) {
	t.Run("should insert without replacing an existing hostname", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		if err := repo.CreateWaypoint("marasi.app:443", "127.0.0.1:8080"); err != nil {
			t.Fatalf("creating waypoint: %v", err)
		}
		err := repo.CreateWaypoint("marasi.app:443", "127.0.0.1:9000")
		if !errors.Is(err, domain.ErrWaypointAlreadyExists) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", domain.ErrWaypointAlreadyExists, err)
		}

		got, err := repo.GetWaypoints()
		if err != nil {
			t.Fatalf("getting waypoints: %v", err)
		}
		if len(got) != 1 || got[0].Override != "127.0.0.1:8080" {
			t.Fatalf("\nwanted:\noriginal waypoint\ngot:\n%v", got)
		}
	})
}

func TestWaypointRepo_CreateOrUpdateWaypoint(t *testing.T) {
	t.Run("should create a new waypoint and save it", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		wantHostname := "marasi.app:443"
		wantOverride := "127.0.0.1:8080"

		err := repo.CreateOrUpdateWaypoint(wantHostname, wantOverride)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetWaypoints()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", len(got))
		}

		if got[0].Hostname != wantHostname {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantHostname, got[0].Hostname)
		}
		if got[0].Override != wantOverride {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantOverride, got[0].Override)
		}

	})

	t.Run("should update an existing waypoint when the hostname matches", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		hostname := "marasi.app:443"
		initialOverride := "127.0.0.1:8080"
		wantOverride := "localhost:9000"

		err := repo.CreateOrUpdateWaypoint(hostname, initialOverride)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.CreateOrUpdateWaypoint(hostname, wantOverride)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		got, err := repo.GetWaypoints()
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(got) != 1 {
			t.Fatalf("\nwanted:\n1\ngot:\n%d", len(got))
		}

		if got[0].Hostname != hostname {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", hostname, got[0].Hostname)
		}

		if got[0].Override == initialOverride {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantOverride, got[0].Override)
		}

		if got[0].Override != wantOverride {
			t.Fatalf("\nwanted:\n%q\ngot:\n%q", wantOverride, got[0].Override)
		}

	})
}

func TestWaypointRepo_UpdateWaypoint(t *testing.T) {
	t.Run("should update only an existing waypoint and report whether it changed", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		if err := repo.CreateWaypoint("marasi.app:443", "127.0.0.1:8080"); err != nil {
			t.Fatalf("creating waypoint: %v", err)
		}
		changed, err := repo.UpdateWaypoint("marasi.app:443", "127.0.0.1:9000")
		if err != nil || !changed {
			t.Fatalf("\nwanted:\nchanged without error\ngot:\nchanged %t, error %v", changed, err)
		}
		changed, err = repo.UpdateWaypoint("marasi.app:443", "127.0.0.1:9000")
		if err != nil || changed {
			t.Fatalf("\nwanted:\nunchanged without error\ngot:\nchanged %t, error %v", changed, err)
		}
	})

	t.Run("should not insert a missing waypoint", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		changed, err := repo.UpdateWaypoint("missing.example:443", "127.0.0.1:9000")
		if !errors.Is(err, domain.ErrNoWaypointForHostname) || changed {
			t.Fatalf("\nwanted:\n%v and unchanged\ngot:\n%v and changed %t", domain.ErrNoWaypointForHostname, err, changed)
		}
		waypoints, getErr := repo.GetWaypoints()
		if getErr != nil || len(waypoints) != 0 {
			t.Fatalf("missing update inserted a waypoint: %v, %v", waypoints, getErr)
		}
	})
}

func TestWaypointRepo_DeleteWaypoint(t *testing.T) {
	t.Run("should delete an existing waypoint", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		hostname := "marasi.app:443"
		override := "127.0.0.1:8080"

		err := repo.CreateOrUpdateWaypoint(hostname, override)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		err = repo.DeleteWaypoint(hostname)
		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		waypoints, err := repo.GetWaypoints()

		if err != nil {
			t.Fatalf("\nwanted:\nnil\ngot:\n%v", err)
		}

		if len(waypoints) != 0 {
			t.Fatalf("\nwanted:\n0\ngot:\n%d", len(waypoints))
		}
	})

	t.Run("should return ErrNoWaypointForHostname when deleting a waypoint that doesn't exist", func(t *testing.T) {
		repo, teardown := setupTestDB(t)
		defer teardown()

		err := repo.DeleteWaypoint("marasi.app:443")

		if !errors.Is(err, domain.ErrNoWaypointForHostname) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", domain.ErrNoWaypointForHostname, err)
		}
	})
}
