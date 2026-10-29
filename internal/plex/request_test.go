package plex

import (
	"net/http/httptest"
	"testing"

	"github.com/braedonsaunders/ether/internal/plex/model"
)

func TestParseRequestNegotiatesFormatFromAccept(t *testing.T) {
	cases := []struct {
		name   string
		accept []string
		want   model.Format
	}{
		{"json only", []string{"application/json"}, model.JSON},
		{"plexapi prefers xml", []string{"application/xml, application/json;q=0.9,*/*;q=0.5"}, model.XML},
		{"plex web", []string{"application/json, text/plain, */*"}, model.JSON},
		{"xml only", []string{"application/xml"}, model.XML},
		{"no accept", nil, model.XML},
		{"wildcard", []string{"*/*"}, model.XML},
		{"json lower q", []string{"application/json;q=0.5, application/xml"}, model.XML},
		{"json higher q", []string{"application/xml;q=0.8, application/json"}, model.JSON},
		{"equal q json first", []string{"application/json, application/xml"}, model.JSON},
		{"equal q xml first", []string{"text/xml, application/json"}, model.XML},
		{"json refused", []string{"application/json;q=0"}, model.XML},
		{"json with charset", []string{"application/json; charset=utf-8"}, model.JSON},
		{"split headers", []string{"application/xml", "application/json;q=0.9"}, model.XML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/identity", nil)
			for _, a := range tc.accept {
				r.Header.Add("Accept", a)
			}
			if got := ParseRequest(r).Format; got != tc.want {
				t.Fatalf("Accept %q: format %v, want %v", tc.accept, got, tc.want)
			}
		})
	}
}
