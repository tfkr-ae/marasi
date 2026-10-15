package db

import (
	"context"
	"maps"
	"testing"

	"github.com/tfkr-ae/marasi/domain"
)

// TestTrafficReads_ReportTheJobsTheirAnswerDependsOn checks the completeness
// each read reports. The traffic index is marked incomplete so a flag's
// value is checked as well as its presence.
func TestTrafficReads_ReportTheJobsTheirAnswerDependsOn(t *testing.T) {
	repo, teardown := setupTestDB(t)
	defer teardown()
	id := capturePair(t, repo, "https://api.x.com/users/1", "application/json", 200)
	launchpadID, err := repo.CreateLaunchpad("completeness", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkRequestToLaunchpad(id, launchpadID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.dbConn.Exec(`UPDATE background_job SET complete = 0 WHERE name = ?`, TrafficIndexJob); err != nil {
		t.Fatal(err)
	}
	search := map[string]bool{domain.JobTrafficIndex: false, domain.JobAnnotationIndex: true}
	searchAndSitemap := map[string]bool{domain.JobTrafficIndex: false, domain.JobAnnotationIndex: true, domain.JobSitemap: true}
	sitemap := map[string]bool{domain.JobSitemap: true}
	asOf := newestID(t, repo)

	tests := []struct {
		name string
		read func() (map[string]bool, error)
		want map[string]bool
	}{
		{"count without the sitemap field", func() (map[string]bool, error) {
			count, err := repo.CountTraffic(domain.TrafficCountQuery{Text: `method = "GET"`})
			return count.Complete, err
		}, search},
		{"count with the sitemap field", func() (map[string]bool, error) {
			count, err := repo.CountTraffic(domain.TrafficCountQuery{Text: `sitemap = "https://api.x.com"`})
			return count.Complete, err
		}, searchAndSitemap},
		{"query-mode sitemap without text terms", func() (map[string]bool, error) {
			page, err := repo.ListSitemapChildren(context.Background(), domain.SitemapQuery{
				Query: &domain.SelectionQuery{Text: `method = "GET"`, AsOf: asOf}, Limit: 10})
			return page.Complete, err
		}, sitemap},
		{"query-mode sitemap with a text term", func() (map[string]bool, error) {
			page, err := repo.ListSitemapChildren(context.Background(), domain.SitemapQuery{
				Query: &domain.SelectionQuery{Text: `"users"`, AsOf: asOf}, Limit: 10})
			return page.Complete, err
		}, searchAndSitemap},
		{"launchpad entries without the sitemap field", func() (map[string]bool, error) {
			_, complete, err := repo.GetLaunchpadRequests(launchpadID, "", nil, "")
			return complete, err
		}, search},
		{"launchpad entries with the sitemap field", func() (map[string]bool, error) {
			_, complete, err := repo.GetLaunchpadRequests(launchpadID, `sitemap = "https://api.x.com"`, nil, "")
			return complete, err
		}, searchAndSitemap},
	}
	for _, tt := range tests {
		t.Run("should report "+tt.name, func(t *testing.T) {
			got, err := tt.read()
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, tt.want) {
				t.Fatalf("\nwanted:\n%v\ngot:\n%v", tt.want, got)
			}
		})
	}
}
