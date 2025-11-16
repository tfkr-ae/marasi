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
			{Hostname: "marasi.app:443", Override: "127.0.0.1:8080"},
			{Hostname: "api.marasi.app:80", Override: "127.0.0.1:9000"},
		}

		for _, waypoint := range want {
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

		if !errors.Is(err, ErrNoWaypointForHostname) {
			t.Fatalf("\nwanted:\n%v\ngot:\n%v", ErrNoWaypointForHostname, err)
		}
	})
}
